// Package sqlitestore is the memory store: one SQLite file, shared by every
// wenna process on the machine (each AI app starts its own). Every method
// validates its input with package memory before touching the database, so no
// caller can skip validation.
//
// History is append-only: remember, update and forget each insert at most
// one event; the current value is derived (schema.sql). Writes run in
// BEGIN IMMEDIATE transactions, which serialise writers across processes, so
// a read-then-insert inside one cannot race.
package sqlitestore

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3" // also registers "sqlite3"

	"github.com/uni-industries/wenna/backend/internal/id"
	"github.com/uni-industries/wenna/backend/internal/memory"
)

//go:embed schema.sql
var schema string

//go:embed schema_v2.sql
var schemaV2 string

// schemaVersion is the user_version the last migration sets. Bump both
// together.
const schemaVersion = 2

// ErrNoFTS5 means this wenna was built without SQLite's full-text search,
// which recall needs: a build without the sqlite_fts5 tag.
var ErrNoFTS5 = errors.New("sqlitestore: this wenna was built without full-text search (build tag sqlite_fts5); rebuild it with make")

// busyTimeout is how long a statement waits for another process's write.
const busyTimeout = 5 * time.Second

// Store reads and writes memory history in one SQLite file.
type Store struct {
	db  *sql.DB
	now func() time.Time
	// trigram keeps and reads the trigram index (trigram.go).
	trigram bool
}

// ErrBusy means another process held the database while Open set it up. It
// is transient: starting again succeeds.
var ErrBusy = errors.New("sqlitestore: another wenna process is setting up the memory database at the same time; start wenna again")

// dbFileSuffixes are the files SQLite keeps beside the database: path plus
// each. All hold memory text.
var dbFileSuffixes = []string{"", "-wal", "-shm", "-journal"}

// ErrNoDir means the database's directory does not exist and was not
// created: see OpenExistingDir.
var ErrNoDir = errors.New("sqlitestore: the database's directory does not exist")

// OpenExistingDir is Open for a path whose directory must already exist,
// such as a mounted volume: a missing one is refused with ErrNoDir rather than
// created on a disk that may not survive a restart.
func OpenExistingDir(ctx context.Context, path string, opts ...Option) (*Store, error) {
	dir := filepath.Dir(path)
	fi, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNoDir, dir)
	}
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: check directory: %w", err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("sqlitestore: %s is not a directory", dir)
	}
	return Open(ctx, path, opts...)
}

// Open opens (creating if needed) the database at path and applies the
// schema. The database and SQLite's files beside it are readable by the
// owner only. A directory Open creates is owner-only; an existing one keeps
// its mode, since it may hold other things.
func Open(ctx context.Context, path string, opts ...Option) (*Store, error) {
	if strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("sqlitestore: path %q contains ? or #", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("sqlitestore: create directory: %w", err)
	}
	// SQLite gives -wal, -shm and -journal the database file's mode, so
	// creating it 0600 first keeps them owner-only from the start.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: create database file: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("sqlitestore: create database file: %w", err)
	}
	q := url.Values{}
	q.Set("_journal_mode", "WAL")
	q.Set("_busy_timeout", fmt.Sprint(busyTimeout.Milliseconds()))
	q.Set("_txlock", "immediate")
	q.Set("_synchronous", "NORMAL")
	db, err := sql.Open("sqlite3", path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: open: %w", err)
	}
	s := &Store{db: db, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	err = requireFTS5(ctx, db)
	if err == nil {
		err = s.migrate(ctx)
	}
	if err != nil {
		if isBusy(err) {
			err = fmt.Errorf("%w (%w)", ErrBusy, err)
		}
		return nil, errors.Join(err, db.Close())
	}
	// Owner-only whatever the umask, including files an earlier wenna left.
	if err := restrict(path); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return s, nil
}

// restrict makes the database and each file SQLite keeps beside it
// owner-only. A file that does not exist is skipped.
func restrict(path string) error {
	for _, suffix := range dbFileSuffixes {
		err := os.Chmod(path+suffix, 0o600)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("sqlitestore: restrict file: %w", err)
		}
	}
	return nil
}

// isBusy reports whether err is SQLite refusing because another connection
// holds the database.
func isBusy(err error) bool {
	var se sqlite3.Error
	return errors.As(err, &se) && (se.Code == sqlite3.ErrBusy || se.Code == sqlite3.ErrLocked)
}

// requireFTS5 refuses a SQLite built without FTS5: recall must not fall back
// to anything quietly.
func requireFTS5(ctx context.Context, db *sql.DB) error {
	var on bool
	if err := db.QueryRowContext(ctx, "SELECT sqlite_compileoption_used('ENABLE_FTS5')").Scan(&on); err != nil {
		return fmt.Errorf("sqlitestore: check full-text search: %w", err)
	}
	if !on {
		return ErrNoFTS5
	}
	return nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// migrate brings the schema to schemaVersion, one migration at a time,
// creates the trigram index when it is on and missing, and brings the index
// up to date with the history, in one write transaction.
func (s *Store) migrate(ctx context.Context) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var v int
		if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
			return fmt.Errorf("sqlitestore: read schema version: %w", err)
		}
		if v > schemaVersion {
			return fmt.Errorf("sqlitestore: database schema is version %d; this wenna knows %d: upgrade wenna", v, schemaVersion)
		}
		if v < 0 {
			return fmt.Errorf("sqlitestore: database schema version %d has no migration", v)
		}
		for _, step := range []string{schema, schemaV2}[v:] {
			if _, err := tx.ExecContext(ctx, step); err != nil {
				return fmt.Errorf("sqlitestore: apply schema: %w", err)
			}
		}
		if s.trigram {
			if err := ensureTrigram(ctx, tx); err != nil {
				return err
			}
		}
		return s.catchUp(ctx, tx)
	})
}

// inTx runs fn in a write transaction (BEGIN IMMEDIATE) and commits if fn
// succeeds.
func (s *Store) inTx(ctx context.Context, fn func(*sql.Tx) error) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlitestore: begin: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("sqlitestore: rollback: %w", rbErr))
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlitestore: commit: %w", err)
	}
	return nil
}

// current is the latest event per memory. SQLite takes the bare columns from
// the row holding max(seq).
const current = `
	SELECT memory_id, kind, content, keywords, created_at, max(seq) AS seq
	FROM memory_events
	GROUP BY memory_id`

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// currentKeywords reports whether id names a memory that exists and is not
// forgotten, and if so its keywords.
func currentKeywords(ctx context.Context, q queryer, id string) (bool, []string, error) {
	var kind string
	var kw sql.NullString
	err := q.QueryRowContext(ctx, "SELECT kind, keywords FROM memory_events WHERE memory_id = ? ORDER BY seq DESC LIMIT 1", id).Scan(&kind, &kw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	ks, err := decodeKeywords(kw)
	return kind == "remember", ks, err
}

// insert appends one event and brings the index up to date with it, in tx.
func (s *Store) insert(ctx context.Context, tx *sql.Tx, memoryID, kind string, content *string, keywords []string) (time.Time, error) {
	at := s.now().UTC()
	kw, err := encodeKeywords(keywords)
	if err != nil {
		return time.Time{}, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO memory_events (memory_id, kind, content, keywords, created_at) VALUES (?, ?, ?, ?, ?)",
		memoryID, kind, content, kw, at.UnixNano()); err != nil {
		return time.Time{}, err
	}
	return at, s.catchUp(ctx, tx)
}

// Remember stores content and keywords and returns its memory. If a current
// memory has exactly this content, that memory is returned as it is and
// nothing is written, keywords included: a retried call, or the same fact
// saved twice, does not create a duplicate.
func (s *Store) Remember(ctx context.Context, content string, keywords []string) (memory.Memory, error) {
	content, err := memory.Content(content)
	if err != nil {
		return memory.Memory{}, err
	}
	if keywords, err = memory.Keywords(keywords); err != nil {
		return memory.Memory{}, err
	}
	var m memory.Memory
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		var at int64
		var kw sql.NullString
		err := tx.QueryRowContext(ctx, `
			SELECT memory_id, keywords, created_at FROM (`+current+`)
			WHERE kind = 'remember' AND content = ?
			ORDER BY created_at, memory_id LIMIT 1`, content).Scan(&m.ID, &kw, &at)
		if err == nil {
			m.Content, m.UpdatedAt = content, time.Unix(0, at).UTC()
			if m.Keywords, err = decodeKeywords(kw); err != nil {
				return fmt.Errorf("sqlitestore: remember: %w", err)
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("sqlitestore: remember: find existing: %w", err)
		}
		m.ID, m.Content, m.Keywords = id.New(), content, nonEmpty(keywords)
		if m.UpdatedAt, err = s.insert(ctx, tx, m.ID, "remember", &content, m.Keywords); err != nil {
			return fmt.Errorf("sqlitestore: remember: %w", err)
		}
		return nil
	})
	if err != nil {
		return memory.Memory{}, err
	}
	return m, nil
}

// Update records new content for an existing memory. Nil keywords keep the
// memory's keywords; an empty slice clears them.
func (s *Store) Update(ctx context.Context, id, content string, keywords []string) (memory.Memory, error) {
	id, err := memory.ID(id)
	if err != nil {
		return memory.Memory{}, err
	}
	if content, err = memory.Content(content); err != nil {
		return memory.Memory{}, err
	}
	if keywords, err = memory.Keywords(keywords); err != nil {
		return memory.Memory{}, err
	}
	m := memory.Memory{ID: id, Content: content, Keywords: nonEmpty(keywords)}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		ok, old, err := currentKeywords(ctx, tx, id)
		if err != nil {
			return fmt.Errorf("sqlitestore: update: %w", err)
		}
		if !ok {
			return memory.ErrNotFound
		}
		if keywords == nil {
			m.Keywords = old
		}
		if m.UpdatedAt, err = s.insert(ctx, tx, id, "remember", &content, m.Keywords); err != nil {
			return fmt.Errorf("sqlitestore: update: %w", err)
		}
		return nil
	})
	if err != nil {
		return memory.Memory{}, err
	}
	return m, nil
}

// Forget records that a memory is gone and returns its normalised id.
func (s *Store) Forget(ctx context.Context, id string) (string, error) {
	id, err := memory.ID(id)
	if err != nil {
		return "", err
	}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		ok, _, err := currentKeywords(ctx, tx, id)
		if err != nil {
			return fmt.Errorf("sqlitestore: forget: %w", err)
		}
		if !ok {
			return memory.ErrNotFound
		}
		if _, err := s.insert(ctx, tx, id, "forget", nil, nil); err != nil {
			return fmt.Errorf("sqlitestore: forget: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// List returns the current memories, most recently changed first.
func (s *Store) List(ctx context.Context, limit int) ([]memory.Memory, error) {
	limit, err := memory.Limit(limit)
	if err != nil {
		return nil, err
	}
	ms, err := s.all(ctx)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: list: %w", err)
	}
	return ms[:min(limit, len(ms))], nil
}

// all returns every current memory, most recently changed first.
func (s *Store) all(ctx context.Context) (out []memory.Memory, err error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT memory_id, content, keywords, created_at FROM (`+current+`)
		WHERE kind = 'remember'
		ORDER BY seq DESC`)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows, &err)
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

type scanner interface{ Scan(dest ...any) error }

// scanMemory reads memory_id, content, keywords, created_at.
func scanMemory(row scanner, extra ...any) (memory.Memory, error) {
	var m memory.Memory
	var kw sql.NullString
	var at int64
	if err := row.Scan(append([]any{&m.ID, &m.Content, &kw, &at}, extra...)...); err != nil {
		return memory.Memory{}, err
	}
	m.UpdatedAt = time.Unix(0, at).UTC()
	var err error
	m.Keywords, err = decodeKeywords(kw)
	return m, err
}

// encodeKeywords is the keywords column's value: a JSON array, or NULL for
// none.
func encodeKeywords(ks []string) (sql.NullString, error) {
	if len(ks) == 0 {
		return sql.NullString{}, nil
	}
	b, err := json.Marshal(ks)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("sqlitestore: encode keywords: %w", err)
	}
	return sql.NullString{String: string(b), Valid: true}, nil
}

func decodeKeywords(v sql.NullString) ([]string, error) {
	if !v.Valid {
		return nil, nil
	}
	var ks []string
	if err := json.Unmarshal([]byte(v.String), &ks); err != nil {
		return nil, fmt.Errorf("sqlitestore: decode keywords: %w", err)
	}
	return nonEmpty(ks), nil
}

// nonEmpty is ks, or nil when it has none: a memory without keywords
// carries nil.
func nonEmpty(ks []string) []string {
	if len(ks) == 0 {
		return nil
	}
	return ks
}

// Export streams every memory event in order. One SELECT reads one
// snapshot. An error from onEvent stops the export.
func (s *Store) Export(ctx context.Context, onEvent func(memory.Event) error) (err error) {
	rows, err := s.db.QueryContext(ctx, "SELECT seq, memory_id, kind, content, keywords, created_at FROM memory_events ORDER BY seq")
	if err != nil {
		return fmt.Errorf("sqlitestore: export: %w", err)
	}
	defer closeRows(rows, &err)
	for rows.Next() {
		var e memory.Event
		var kw sql.NullString
		var at int64
		if err := rows.Scan(&e.Seq, &e.MemoryID, &e.Kind, &e.Content, &kw, &at); err != nil {
			return fmt.Errorf("sqlitestore: export: scan: %w", err)
		}
		if e.Keywords, err = decodeKeywords(kw); err != nil {
			return fmt.Errorf("sqlitestore: export: %w", err)
		}
		e.At = time.Unix(0, at).UTC()
		if err := onEvent(e); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sqlitestore: export: %w", err)
	}
	return nil
}

// closeRows closes rows and joins its error into *err.
func closeRows(rows *sql.Rows, err *error) {
	if cErr := rows.Close(); cErr != nil {
		*err = errors.Join(*err, fmt.Errorf("sqlitestore: close rows: %w", cErr))
	}
}
