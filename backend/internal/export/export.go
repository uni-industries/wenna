// Package export writes the user's whole memory history as one gzipped JSON
// file (CLAUDE.md § Decisions, Export): to a writer (Write, for the CLI) or
// over HTTP (Handler). The store reads one consistent snapshot.
//
// Handler builds the file into a temporary file while the snapshot is open,
// and sends it only after it closes: a slow or stalled download never holds
// the database. One export runs at a time.
//
// Format (version 1):
//
//	{"format":"wenna-export","version":1,"exported_at":"…",
//	 "events":[{"seq":1,"memory_id":"…","kind":"remember","content":"…","at":"…"}, …]}
//
// events is the full memory history in order; a memory's current value is its
// last event, and a "forget" (no content) means it is gone.
package export

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/uni-industries/wenna/backend/internal/memory"
)

// Format and Version identify the file's layout.
const (
	Format  = "wenna-export"
	Version = 1
)

const (
	// retryAfter is what a refused export is told to wait, in seconds.
	retryAfter = "30"
	// chunkWindow is how long one chunk of the download may take. Refreshed
	// per chunk, so a reader that stops is dropped within it, while a slow
	// but moving download may take as long as it needs.
	chunkWindow = 30 * time.Second
	chunkSize   = 64 << 10
)

// Store reads the memory history in one snapshot.
type Store interface {
	Export(ctx context.Context, onEvent func(memory.Event) error) error
}

// Write writes the export file, stamped at, to w.
func Write(ctx context.Context, store Store, at time.Time, w io.Writer) error {
	_, err := build(ctx, store, at, w)
	return err
}

// Handler serves the export. tmpDir holds files while they are built; ""
// means the system default.
func Handler(store Store, tmpDir string, now func() time.Time, logger *slog.Logger) http.Handler {
	// One export at a time: the slot covers the build and the download.
	slot := make(chan struct{}, 1)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case slot <- struct{}{}:
		default:
			busyResponse(w, "An export is already running. Try again shortly.")
			return
		}
		defer func() { <-slot }()
		started := now()
		f, size, err := buildFile(r.Context(), store, started, tmpDir)
		if err != nil {
			fail(w, r, logger, err)
			return
		}
		defer func() {
			if err := f.Close(); err != nil {
				logger.WarnContext(r.Context(), "close export temp file", "err", err)
			}
		}()
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			fail(w, r, logger, fmt.Errorf("export: rewind: %w", err))
			return
		}

		h := w.Header()
		h.Set("Content-Type", "application/gzip")
		h.Set("Content-Length", strconv.FormatInt(size, 10))
		h.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="wenna-export-%s.json.gz"`, started.UTC().Format("20060102")))
		h.Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		if err := send(r.Context(), w, f, now, logger); err != nil {
			// The client went away or stalled mid-download. Content-Length
			// tells it the file is short; abort so nothing presents it as
			// complete.
			logger.InfoContext(r.Context(), "export download interrupted", "err", err)
			panic(http.ErrAbortHandler)
		}
	})
}

// buildFile writes the export to an unlinked temp file and returns it
// rewound, with its size. Unlinked at once, so no exit path, a crash or kill
// included, leaves a user's history on disk: the data lives while the handle
// is open.
func buildFile(ctx context.Context, store Store, at time.Time, dir string) (*os.File, int64, error) {
	f, err := os.CreateTemp(dir, "wenna-export-*.json.gz")
	if err != nil {
		return nil, 0, fmt.Errorf("export: temp file: %w", err)
	}
	if err := os.Remove(f.Name()); err != nil {
		return nil, 0, errors.Join(fmt.Errorf("export: unlink temp file: %w", err), f.Close())
	}
	size, err := build(ctx, store, at, f)
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	if err != nil {
		return nil, 0, errors.Join(err, f.Close())
	}
	return f, size, nil
}

// send copies f to w a chunk at a time, each under its own write deadline.
func send(ctx context.Context, w http.ResponseWriter, f io.Reader, now func() time.Time, logger *slog.Logger) error {
	rc := http.NewResponseController(w)
	buf := make([]byte, chunkSize)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			if err := rc.SetWriteDeadline(now().Add(chunkWindow)); err != nil && !errors.Is(err, http.ErrNotSupported) {
				logger.WarnContext(ctx, "set export write deadline", "err", err)
			}
			if _, err := w.Write(buf[:n]); err != nil {
				return err
			}
		}
		if errors.Is(rerr, io.EOF) {
			return nil
		}
		if rerr != nil {
			return fmt.Errorf("export: read temp file: %w", rerr)
		}
	}
}

// build writes the whole file to out and returns its size.
func build(ctx context.Context, store Store, at time.Time, out io.Writer) (int64, error) {
	cw := &countingWriter{w: out}
	doc := newFile(cw)
	if err := doc.begin(at); err != nil {
		return 0, err
	}
	if err := store.Export(ctx, doc.event); err != nil {
		return 0, err
	}
	if err := doc.end(); err != nil {
		return 0, err
	}
	return cw.n, nil
}

func busyResponse(w http.ResponseWriter, msg string) {
	w.Header().Set("Retry-After", retryAfter)
	http.Error(w, msg, http.StatusServiceUnavailable)
}

func fail(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	logger.ErrorContext(r.Context(), "export failed", "err", err)
	http.Error(w, "Export is unavailable right now.", http.StatusInternalServerError)
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// file writes the JSON document through gzip, one event at a time.
type file struct {
	zw     *gzip.Writer
	enc    *json.Encoder
	events int
}

func newFile(w io.Writer) *file {
	zw := gzip.NewWriter(w)
	return &file{zw: zw, enc: json.NewEncoder(zw)}
}

func (f *file) raw(s string) error {
	_, err := io.WriteString(f.zw, s)
	return err
}

func (f *file) begin(at time.Time) error {
	head, err := json.Marshal(struct {
		Format     string    `json:"format"`
		Version    int       `json:"version"`
		ExportedAt time.Time `json:"exported_at"`
	}{Format, Version, at.UTC()})
	if err != nil {
		return fmt.Errorf("export: header: %w", err)
	}
	// Reopen the object to append the events array.
	if err := f.raw(string(head[:len(head)-1]) + `,"events":[`); err != nil {
		return fmt.Errorf("export: write: %w", err)
	}
	return nil
}

func (f *file) event(e memory.Event) error {
	if f.events > 0 {
		if err := f.raw(","); err != nil {
			return fmt.Errorf("export: write: %w", err)
		}
	}
	f.events++
	if err := f.enc.Encode(e); err != nil {
		return fmt.Errorf("export: event %d: %w", e.Seq, err)
	}
	return nil
}

func (f *file) end() error {
	if err := f.raw("]}\n"); err != nil {
		return fmt.Errorf("export: write: %w", err)
	}
	if err := f.zw.Close(); err != nil {
		return fmt.Errorf("export: finish gzip: %w", err)
	}
	return nil
}
