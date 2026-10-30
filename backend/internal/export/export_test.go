package export

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/uni-industries/wenna/backend/internal/memory"
)

var now = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

type fakeStore struct {
	mu         sync.Mutex
	events     []memory.Event
	failFirst  error         // before any event
	failAfter  error         // after the first event
	entered    chan struct{} // signalled when an export starts
	release    chan struct{} // when set, Export waits for it
	enterCount int
}

func (f *fakeStore) Export(_ context.Context, onEvent func(memory.Event) error) error {
	f.mu.Lock()
	f.enterCount++
	entered, release, failFirst := f.entered, f.release, f.failFirst
	f.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if release != nil {
		<-release
	}
	if failFirst != nil {
		return failFirst
	}
	for i, e := range f.events {
		if i == 1 && f.failAfter != nil {
			return f.failAfter
		}
		if err := onEvent(e); err != nil {
			return err
		}
	}
	return nil
}

func ptr(s string) *string { return &s }

func request() *http.Request { return httptest.NewRequest(http.MethodGet, "/api/export", nil) }

func newHandler(t *testing.T, store Store) (http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	return Handler(store, dir, func() time.Time { return now }, slog.New(slog.DiscardHandler)), dir
}

func serve(t *testing.T, store Store) (*httptest.ResponseRecorder, string) {
	t.Helper()
	h, dir := newHandler(t, store)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, request())
	return rec, dir
}

// assertNoTempFiles: every exit path removes the file it built.
func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	left, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

type document struct {
	Format     string         `json:"format"`
	Version    int            `json:"version"`
	ExportedAt time.Time      `json:"exported_at"`
	Events     []memory.Event `json:"events"`
}

func decode(t *testing.T, body []byte) document {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("not gzip: %v", err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	var d document
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		t.Fatalf("not the export JSON: %v\n%s", err, raw)
	}
	return d
}

var history = []memory.Event{
	{Seq: 1, MemoryID: "01A", Kind: "remember", Content: ptr(`likes "tea"` + "\n<b>"), At: now.Add(-3 * time.Minute)},
	{Seq: 2, MemoryID: "01A", Kind: "remember", Content: ptr("likes coffee"), At: now.Add(-2 * time.Minute)},
	{Seq: 3, MemoryID: "01A", Kind: "forget", At: now.Add(-time.Minute)},
}

func checkDocument(t *testing.T, d document) {
	t.Helper()
	if d.Format != Format || d.Version != Version || !d.ExportedAt.Equal(now) {
		t.Errorf("header = %+v", d)
	}
	if len(d.Events) != 3 || *d.Events[0].Content != *history[0].Content || d.Events[2].Content != nil || d.Events[2].Kind != "forget" {
		t.Errorf("events = %+v", d.Events)
	}
}

func TestExport(t *testing.T) {
	t.Parallel()
	rec, dir := serve(t, &fakeStore{events: history})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	h := rec.Header()
	if h.Get("Content-Type") != "application/gzip" ||
		h.Get("Content-Disposition") != `attachment; filename="wenna-export-20260301.json.gz"` ||
		h.Get("Cache-Control") != "no-store" ||
		h.Get("Content-Length") != strconv.Itoa(rec.Body.Len()) {
		t.Errorf("headers = %v (body %d bytes)", h, rec.Body.Len())
	}
	checkDocument(t, decode(t, rec.Body.Bytes()))
	assertNoTempFiles(t, dir)
}

func TestWrite(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := Write(context.Background(), &fakeStore{events: history}, now, &buf); err != nil {
		t.Fatal(err)
	}
	checkDocument(t, decode(t, buf.Bytes()))

	boom := errors.New("boom")
	if err := Write(context.Background(), &fakeStore{failFirst: boom}, now, io.Discard); !errors.Is(err, boom) {
		t.Errorf("Write = %v, want the store's error", err)
	}
}

func TestEmptyHistory(t *testing.T) {
	t.Parallel()
	rec, _ := serve(t, &fakeStore{})
	d := decode(t, rec.Body.Bytes())
	if d.Events == nil || len(d.Events) != 0 {
		t.Errorf("events = %#v, want an empty array", d.Events)
	}
}

// TestStoreFailure: whenever the store fails, before or after events, the
// client gets a plain 500 and no file: nothing is sent until the file is
// whole.
func TestStoreFailure(t *testing.T) {
	t.Parallel()
	for name, store := range map[string]*fakeStore{
		"before data": {failFirst: errors.New("open /home/u/.wenna/wenna.db: disk I/O error")},
		"mid-history": {
			events:    []memory.Event{{Seq: 1, MemoryID: "01A", Kind: "remember", Content: ptr("x"), At: now}, {Seq: 2}},
			failAfter: errors.New("open /home/u/.wenna/wenna.db: disk I/O error"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rec, dir := serve(t, store)
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", rec.Code)
			}
			if strings.Contains(rec.Body.String(), "wenna.db") || rec.Header().Get("Content-Disposition") != "" {
				t.Errorf("failure leaks plumbing or offers a file: %q %v", rec.Body.String(), rec.Header())
			}
			assertNoTempFiles(t, dir)
		})
	}
}

// failingWriter accepts headers, then fails the body: a client that left.
type failingWriter struct{ h http.Header }

func (f *failingWriter) Header() http.Header       { return f.h }
func (f *failingWriter) WriteHeader(int)           {}
func (f *failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestInterruptedDownloadAborts(t *testing.T) {
	t.Parallel()
	h, dir := newHandler(t, &fakeStore{})
	func() {
		defer func() {
			if r := recover(); r != http.ErrAbortHandler {
				t.Fatalf("recovered %v, want http.ErrAbortHandler", r)
			}
		}()
		h.ServeHTTP(&failingWriter{h: http.Header{}}, request())
		t.Fatal("handler returned normally after the download failed")
	}()
	assertNoTempFiles(t, dir)
}

// TestOneExportAtATime: while an export runs, another is refused without
// touching the store; once it ends, exports work again.
func TestOneExportAtATime(t *testing.T) {
	t.Parallel()
	store := &fakeStore{entered: make(chan struct{}, 2), release: make(chan struct{})}
	h, _ := newHandler(t, store)

	done := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, request())
		done <- rec.Code
	}()
	select {
	case <-store.entered:
	case code := <-done: // hang guard: refused instead of entering the store
		t.Fatalf("first export never reached the store: %d", code)
	}

	second := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, request())
		second <- rec
	}()
	select {
	case rec := <-second:
		if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
			t.Errorf("second export: %d %v, want 503 with Retry-After", rec.Code, rec.Header())
		}
	case <-time.After(30 * time.Second): // hang guard: the first export never finishes before this
		close(store.release)
		t.Fatal("second export waited on the first instead of being refused")
	}
	close(store.release)
	if code := <-done; code != http.StatusOK {
		t.Errorf("first export = %d", code)
	}
	store.mu.Lock()
	entered := store.enterCount
	store.mu.Unlock()
	if entered != 1 {
		t.Errorf("store entered %d times, want 1: the refused export reached it", entered)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, request())
	if rec.Code != http.StatusOK {
		t.Errorf("export after the first finished = %d", rec.Code)
	}
}

// TestTempFileUnlinkedWhileBuilding: the file is gone from the directory
// before any data is written, so a crash mid-export leaves nothing on disk.
func TestTempFileUnlinkedWhileBuilding(t *testing.T) {
	t.Parallel()
	store := &fakeStore{entered: make(chan struct{}, 1), release: make(chan struct{})}
	h, dir := newHandler(t, store)
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(httptest.NewRecorder(), request())
		close(done)
	}()
	select {
	case <-store.entered:
	case <-done: // hang guard: refused instead of entering the store
		t.Fatal("export never reached the store")
	}
	assertNoTempFiles(t, dir)
	close(store.release)
	<-done
}

// TestSlotReleasedAfterFailure: failures in a row never lock exports out.
func TestSlotReleasedAfterFailure(t *testing.T) {
	t.Parallel()
	store := &fakeStore{failFirst: errors.New("boom")}
	h, _ := newHandler(t, store)
	for i := range 3 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, request())
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("failure %d: status %d, want 500 (a 503 means a leaked slot)", i, rec.Code)
		}
	}
	store.mu.Lock()
	store.failFirst = nil
	store.mu.Unlock()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, request())
	if rec.Code != http.StatusOK {
		t.Fatalf("export after failures = %d, want 200", rec.Code)
	}
}

// TestStalledReaderDropped: on a real server, a download whose chunk
// misses its write deadline is cut off, never completed. The clock is set
// back so every deadline is already past.
func TestStalledReaderDropped(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("x", 4000)
	var events []memory.Event
	for i := range 200 {
		events = append(events, memory.Event{Seq: int64(i + 1), MemoryID: "01A", Kind: "remember", Content: ptr(big + strconv.Itoa(i)), At: now})
	}
	past := func() time.Time { return time.Now().Add(-time.Hour) }
	srv := httptest.NewServer(Handler(&fakeStore{events: events}, t.TempDir(), past, slog.New(slog.DiscardHandler)))
	t.Cleanup(srv.Close)

	res, err := http.Get(srv.URL)
	if err != nil {
		return // cut before the status line: also a failed download
	}
	defer func() {
		if err := res.Body.Close(); err != nil {
			t.Log(err)
		}
	}()
	if _, err := io.ReadAll(res.Body); err == nil {
		t.Fatal("download completed although every write deadline had passed")
	}
}
