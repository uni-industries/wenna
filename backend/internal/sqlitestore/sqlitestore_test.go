package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"

	"github.com/uni-industries/wenna/backend/internal/id"
	"github.com/uni-industries/wenna/backend/internal/memory"
)

func open(t *testing.T, path string, opts ...Option) *Store {
	t.Helper()
	s, err := Open(context.Background(), path, opts...)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

func newStore(t *testing.T, opts ...Option) *Store {
	t.Helper()
	return open(t, filepath.Join(t.TempDir(), "wenna.db"), opts...)
}

// eachMode runs test as a parallel subtest with the trigram index off, then
// on: one body, both modes.
func eachMode(t *testing.T, test func(t *testing.T, trigram bool)) {
	t.Helper()
	t.Parallel()
	for _, on := range []bool{false, true} {
		t.Run(fmt.Sprintf("trigram=%v", on), func(t *testing.T) {
			t.Parallel()
			test(t, on)
		})
	}
}

func ids(ms []memory.Memory) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Content)
	}
	return out
}

func TestRememberUpdateForget(t *testing.T) { eachMode(t, testRememberUpdateForget) }

func testRememberUpdateForget(t *testing.T, trigram bool) {
	ctx := context.Background()
	s := newStore(t, WithTrigram(trigram))

	m, err := s.Remember(ctx, "  likes green tea  ", nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Content != "likes green tea" || len(m.ID) != 26 || m.UpdatedAt.IsZero() {
		t.Fatalf("remember = %+v", m)
	}
	again, err := s.Remember(ctx, "likes green tea", nil)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != m.ID {
		t.Errorf("same content saved twice: %s and %s", m.ID, again.ID)
	}

	u, err := s.Update(ctx, strings.ToLower(m.ID), "likes black tea", nil)
	if err != nil {
		t.Fatal(err)
	}
	if u.ID != m.ID || u.Content != "likes black tea" {
		t.Errorf("update = %+v", u)
	}
	// The old text is no longer current, so saving it is a new memory.
	if old, err := s.Remember(ctx, "likes green tea", nil); err != nil || old.ID == m.ID {
		t.Errorf("remember old text = %+v, %v; want a new memory", old, err)
	}

	got, err := s.Forget(ctx, m.ID)
	if err != nil || got != m.ID {
		t.Fatalf("forget = %q, %v", got, err)
	}
	if _, err := s.Forget(ctx, m.ID); !errors.Is(err, memory.ErrNotFound) {
		t.Errorf("forget twice: %v, want ErrNotFound", err)
	}
	if _, err := s.Update(ctx, m.ID, "x", nil); !errors.Is(err, memory.ErrNotFound) {
		t.Errorf("update forgotten: %v, want ErrNotFound", err)
	}
	if _, err := s.Update(ctx, id.New(), "x", nil); !errors.Is(err, memory.ErrNotFound) {
		t.Errorf("update unknown: %v, want ErrNotFound", err)
	}
	list, err := s.List(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(list); len(got) != 1 || got[0] != "likes green tea" {
		t.Errorf("list = %v, want only the re-saved memory", got)
	}
	checkIndex(t, s)
}

func TestInvalidInputNeverWrites(t *testing.T) { eachMode(t, testInvalidInputNeverWrites) }

func testInvalidInputNeverWrites(t *testing.T, trigram bool) {
	ctx := context.Background()
	s := newStore(t, WithTrigram(trigram))
	checks := map[string]error{}
	_, checks["empty content"] = s.Remember(ctx, "   ", nil)
	_, checks["long content"] = s.Remember(ctx, strings.Repeat("a", memory.MaxContentRunes+1), nil)
	_, checks["bad id"] = s.Update(ctx, "not-an-id", "x", nil)
	_, checks["bad forget id"] = s.Forget(ctx, "")
	_, checks["empty query"] = s.Recall(ctx, memory.RecallRequest{Query: " ", Limit: 0})
	_, checks["limit"] = s.List(ctx, memory.MaxLimit+1)
	_, checks["bad keyword"] = s.Remember(ctx, "x", []string{"\x00"})
	_, checks["too many keywords"] = s.Remember(ctx, "x", make([]string, memory.MaxKeywords+1))
	_, checks["update bad keyword"] = s.Update(ctx, id.New(), "x", []string{" "})
	_, checks["time weight"] = s.Recall(ctx, memory.RecallRequest{Query: "x", TimeWeight: "x"})
	_, checks["order"] = s.Recall(ctx, memory.RecallRequest{Query: "x", Order: "x"})
	for name, err := range checks {
		if !errors.Is(err, memory.ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	var n int
	if err := s.db.QueryRow("SELECT count(*) FROM memory_events").Scan(&n); err != nil || n != 0 {
		t.Errorf("events = %d, %v; want 0", n, err)
	}
}

func TestListOrderAndLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)
	first, err := s.Remember(ctx, "first", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"second", "third"} {
		if _, err := s.Remember(ctx, c, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Update(ctx, first.ID, "first, changed", nil); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ids(list), "|"); got != "first, changed|third" {
		t.Errorf("list = %s, want most recently changed first, limited to 2", got)
	}
}

func TestRecall(t *testing.T) { eachMode(t, testRecall) }

func testRecall(t *testing.T, trigram bool) {
	ctx := context.Background()
	s := newStore(t, WithTrigram(trigram))
	for _, c := range []string{
		"Prefers window seats on long flights",
		"Allergic to peanuts",
		"Daughter's name is Zoë",
		"Long flights make them tired; window seat helps",
		"Works at Acme on the billing team",
		"Green tea in the morning",
		"Tea before bed, never green",
		"Lives on Hauptstraße",
		"Favourite book is Οδυσσευς",
		"Keeps every ﬁle in one folder",
	} {
		if _, err := s.Remember(ctx, c, nil); err != nil {
			t.Fatal(err)
		}
	}
	forgotten, err := s.Remember(ctx, "window seats are a must", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Forget(ctx, forgotten.ID); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		query string
		want  string // joined contents, best first
	}{
		// Phrase beats words; among equal scores, most recent first.
		{"window seats", "Prefers window seats on long flights|Long flights make them tired; window seat helps"},
		{"PEANUTS", "Allergic to peanuts"},
		{"zoë", "Daughter's name is Zoë"},
		{"ZOË", "Daughter's name is Zoë"},
		{"acme, billing!", "Works at Acme on the billing team"},
		// Same words, one has them as the phrase: it wins over the newer one.
		// Words match as substrings, so "tea" also finds "team", ranked last.
		{"green tea", "Green tea in the morning|Tea before bed, never green|Works at Acme on the billing team"},
		{"nothing matches", ""},
		// Other forms of a word match on its first five letters.
		{"allergies", "Allergic to peanuts"},
		{"daughters", "Daughter's name is Zoë"},
		// Under five letters, substring only: "zoe" is not "zoë".
		{"zoe", ""},
		// Case folding beyond ASCII. FTS5 folds one character to one, so
		// ß does not match ss, nor ﬁ fi.
		{"ΟΔΥΣΣΕΥΣ", "Favourite book is Οδυσσευς"},
	}
	for _, tc := range tests {
		got, err := s.Recall(ctx, memory.RecallRequest{Query: tc.query, Limit: 0})
		if err != nil {
			t.Fatal(err)
		}
		if g := strings.Join(ids(got), "|"); g != tc.want {
			t.Errorf("recall(%q) = %q, want %q", tc.query, g, tc.want)
		}
	}
	if got, err := s.Recall(ctx, memory.RecallRequest{Query: "flights", Limit: 1}); err != nil || len(got) != 1 {
		t.Errorf("recall limit 1 = %v, %v", got, err)
	}
}

// TestRecallRelated: memories sharing a name with the best matches follow
// them, ahead of weaker matches, so "first customer" also finds who the
// contact there is.
func TestRecallRelated(t *testing.T) { eachMode(t, testRecallRelated) }

func testRecallRelated(t *testing.T, trigram bool) {
	ctx := context.Background()
	s := newStore(t, WithTrigram(trigram))
	for _, c := range []string{
		"Lunch with Sam and I on Friday",
		"Dinner with Jo and I on Monday",
		"Apple pie on Sundays",
		"Lumen Labs has 7 employees",
		"Lumen Labs uses Postgres",
		"Lumen Labs banks with Mercury",
		"Lumen Labs is in Austin",
		"Prefers short answers",
		"Prefers tea",
		"Uses Apple Notes for drafts",
		"Eats an apple every day",
		"The customer support tool is Intercom",
		"Our contact at Acme Robotics is Dana Whitfield",
		"Acme Robotics renews on March 1",
		"Our first customer is Acme Robotics",
	} {
		if _, err := s.Remember(ctx, c, nil); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		query string
		want  string // joined contents, best first
	}{
		// Shared names, then the weaker "customer" match; a name that only
		// leads a memory ("Acme" here) counts once it is capitalized mid-text.
		{"first customer", "Our first customer is Acme Robotics|Acme Robotics renews on March 1|Our contact at Acme Robotics is Dana Whitfield|The customer support tool is Intercom"},
		// Lumen and Labs are in 4 of 15 memories: common, not followed.
		{"Postgres", "Lumen Labs uses Postgres"},
		// Apple is also a lower-case word: not a name, so the pie stays out.
		{"drafts", "Uses Apple Notes for drafts"},
		// A one-letter word is not a name.
		{"lunch", "Lunch with Sam and I on Friday"},
		// A capitalized first word that is never capitalized elsewhere is not a name.
		{"short answers", "Prefers short answers"},
	}
	for _, tc := range tests {
		got, err := s.Recall(ctx, memory.RecallRequest{Query: tc.query, Limit: 0})
		if err != nil {
			t.Fatal(err)
		}
		if g := strings.Join(ids(got), "|"); g != tc.want {
			t.Errorf("recall(%q) = %q, want %q", tc.query, g, tc.want)
		}
	}
	if got, err := s.Recall(ctx, memory.RecallRequest{Query: "first customer", Limit: 2}); err != nil || strings.Join(ids(got), "|") != "Our first customer is Acme Robotics|Acme Robotics renews on March 1" {
		t.Errorf("recall limit 2 = %q, %v", ids(got), err)
	}
}

func TestExportHasFullHistory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)
	m, err := s.Remember(ctx, "v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(ctx, m.ID, "v2", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Forget(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	var got []string
	err = s.Export(ctx, func(e memory.Event) error {
		c := "<nil>"
		if e.Content != nil {
			c = *e.Content
		}
		if e.MemoryID != m.ID || e.At.IsZero() {
			t.Errorf("event = %+v", e)
		}
		got = append(got, e.Kind+":"+c)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if g := strings.Join(got, "|"); g != "remember:v1|remember:v2|forget:<nil>" {
		t.Errorf("export = %s", g)
	}
	stop := errors.New("stop")
	if err := s.Export(ctx, func(memory.Event) error { return stop }); !errors.Is(err, stop) {
		t.Errorf("callback error = %v, want it returned", err)
	}
}

// Several processes (one per AI app) share the file; two Stores on one path
// stand in for them: both with the trigram index off, both on, and one of
// each.
func TestConcurrentWritersShareOneFile(t *testing.T) {
	t.Parallel()
	for _, mode := range [][2]bool{{false, false}, {true, true}, {true, false}} {
		t.Run(fmt.Sprintf("trigram=%v,%v", mode[0], mode[1]), func(t *testing.T) {
			t.Parallel()
			testConcurrentWriters(t, mode[0], mode[1])
		})
	}
}

func testConcurrentWriters(t *testing.T, triA, triB bool) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "wenna.db")
	a, b := open(t, path, WithTrigram(triA)), open(t, path, WithTrigram(triB))

	const n = 20
	var wg sync.WaitGroup
	got := make([]string, 2*n)
	errs := make([]error, 2*n)
	for i := range 2 * n {
		s := a
		if i%2 == 1 {
			s = b
		}
		wg.Go(func() {
			m, err := s.Remember(ctx, "the same fact", nil)
			got[i], errs[i] = m.ID, err
		})
	}
	wg.Wait()
	for i := range got {
		if errs[i] != nil {
			t.Fatalf("remember %d: %v", i, errs[i])
		}
		if got[i] != got[0] {
			t.Fatalf("concurrent identical remembers made two memories: %s and %s", got[0], got[i])
		}
	}

	// Forget and update race on one memory: after both, the memory is either
	// forgotten (update lost with ErrNotFound) or updated then forgotten,
	// never resurrected.
	for range n {
		m, err := a.Remember(ctx, "racing "+id.New(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var fErr, uErr error
		wg.Go(func() { _, fErr = a.Forget(ctx, m.ID) })
		wg.Go(func() { _, uErr = b.Update(ctx, m.ID, "updated "+id.New(), nil) })
		wg.Wait()
		if fErr != nil {
			t.Fatalf("forget: %v", fErr)
		}
		if uErr != nil && !errors.Is(uErr, memory.ErrNotFound) {
			t.Fatalf("update: %v", uErr)
		}
		if ok, _, err := currentKeywords(ctx, a.db, m.ID); err != nil || ok {
			t.Fatalf("memory current after forget: %v %v", ok, err)
		}
	}
	// b's writes reach a's trigram index, when a has one, as a recall would
	// bring them.
	if err := a.catchUpIfBehind(ctx); err != nil {
		t.Fatal(err)
	}
	checkIndex(t, a)
	checkIndex(t, b)
}

// OpenExistingDir refuses a missing directory instead of creating it (an
// unmounted volume), and opens normally in one that exists.
func TestOpenExistingDir(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	missing := filepath.Join(t.TempDir(), "unmounted")
	if s, err := OpenExistingDir(ctx, filepath.Join(missing, "wenna.db")); !errors.Is(err, ErrNoDir) {
		t.Fatalf("OpenExistingDir(missing) = %v, %v; want ErrNoDir", s, err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing directory was created: %v", err)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := OpenExistingDir(ctx, filepath.Join(file, "wenna.db")); err == nil {
		t.Fatalf("OpenExistingDir under a file succeeded; close: %v", s.Close())
	}
	s, err := OpenExistingDir(ctx, filepath.Join(t.TempDir(), "wenna.db"))
	if err != nil {
		t.Fatalf("OpenExistingDir(existing) = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "nested", "dir")
	path := filepath.Join(dir, "wenna.db")
	s := open(t, path)
	if _, err := s.Remember(ctx, "kept across opens", nil); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]os.FileMode{dir: 0o700, path: 0o600} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", p, got, want)
		}
	}
	// Reopening applies nothing and keeps data.
	again := open(t, path)
	if list, err := again.List(ctx, 0); err != nil || len(list) != 1 {
		t.Errorf("after reopen: %v, %v", list, err)
	}

	if _, err := Open(ctx, filepath.Join(t.TempDir(), "a?b.db")); err == nil {
		t.Error("path with ? accepted")
	}
}

func assertOwnerOnly(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			t.Error(err)
			continue
		}
		if got := fi.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %o, want 600", p, got)
		}
	}
}

// The database and the WAL files beside it hold memory text: owner-only even
// in a shared directory, which Open leaves alone.
func TestOpenFilesOwnerOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "wenna.db")
	s := open(t, path)
	if _, err := s.Remember(ctx, "secret", nil); err != nil {
		t.Fatal(err)
	}
	assertOwnerOnly(t, path, path+"-wal", path+"-shm")
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("existing directory changed: %v, %v", fi, err)
	}
}

// Files an earlier wenna left readable are tightened on the next Open.
func TestOpenTightensExistingFiles(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "wenna.db")
	// Hold one Store open so its -wal and -shm survive while files are loosened.
	s := open(t, path)
	if _, err := s.Remember(context.Background(), "secret", nil); err != nil {
		t.Fatal(err)
	}
	files := []string{path, path + "-wal", path + "-shm"}
	for _, p := range files {
		if err := os.Chmod(p, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	open(t, path)
	assertOwnerOnly(t, files...)
}

// The database file is owner-only before SQLite first touches it, so the
// files SQLite creates beside it inherit 0600 and there is no window in which
// it is readable. A cancelled Open stops before SQLite connects.
func TestOpenCreatesFileOwnerOnly(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path := filepath.Join(t.TempDir(), "wenna.db")
	if s, err := Open(ctx, path); err == nil {
		t.Fatalf("Open with cancelled context succeeded; close: %v", s.Close())
	}
	assertOwnerOnly(t, path)
}

// Another process holding a brand-new database while Open sets it up is
// transient: Open says so, quickly, and the person starts wenna again.
func TestOpenBusyTellsToRetry(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "wenna.db")
	holder, err := sql.Open("sqlite3", path+"?_txlock=exclusive")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := holder.Close(); err != nil {
			t.Errorf("close holder: %v", err)
		}
	})
	tx, err := holder.Begin()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tx.Rollback(); err != nil {
			t.Errorf("rollback holder: %v", err)
		}
	})
	start := time.Now()
	s, err := Open(context.Background(), path)
	if err == nil {
		t.Fatalf("Open succeeded while the database was held; close: %v", s.Close())
	}
	if !errors.Is(err, ErrBusy) {
		t.Errorf("Open = %v, want ErrBusy", err)
	}
	if !strings.Contains(err.Error(), "start wenna again") {
		t.Errorf("Open = %q, want it to say to start again", err)
	}
	if d := time.Since(start); d > busyTimeout+time.Second {
		t.Errorf("Open took %v", d)
	}
}

func TestIsBusy(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{fmt.Errorf("begin: %w", sqlite3.Error{Code: sqlite3.ErrBusy}), true},
		{fmt.Errorf("begin: %w", sqlite3.Error{Code: sqlite3.ErrLocked}), true},
		{sqlite3.Error{Code: sqlite3.ErrCorrupt}, false},
		{errors.New("database is locked"), false},
	} {
		if got := isBusy(tc.err); got != tc.want {
			t.Errorf("isBusy(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestOpenRefusesNewerSchema(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "wenna.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion+1)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), path)
	if err == nil {
		t.Errorf("Open accepted a newer schema; close: %v", s.Close())
	} else if !strings.Contains(err.Error(), "upgrade wenna") {
		t.Errorf("Open = %v, want refusal naming the upgrade", err)
	}
}
