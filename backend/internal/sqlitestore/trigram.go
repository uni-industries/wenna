package sqlitestore

import (
	"context"
	"database/sql"
	"fmt"
)

// The trigram index, memory_trigram, holds each current memory's text by
// every three letters, so text inside a word matches ("foods" finds
// "atlasfoods.com"). It is optional (WithTrigram) and outside the versioned
// schema: it makes the database larger and writes slower. A
// store with it off never creates, reads or writes it, and leaves one that
// exists as it is.
//
// Processes sharing the file may differ in the option, so the index keeps its
// own position, trigram_state: the index_state seq it reflects. A store with
// it on brings it up to date with the word index in catchUp, the one path
// that changes it.

// Option configures Open.
type Option func(*Store)

// WithTrigram turns the trigram index on or off; off is the default.
func WithTrigram(on bool) Option { return func(s *Store) { s.trigram = on } }

// trigramSchema creates the trigram index, empty, at position 0. Same rowids
// as memories.
const trigramSchema = `
CREATE VIRTUAL TABLE memory_trigram USING fts5(
    content, keywords,
    content = '', contentless_delete = 1,
    tokenize = 'trigram'
);
CREATE TABLE trigram_state (
    one INTEGER PRIMARY KEY CHECK (one = 1),
    seq INTEGER NOT NULL
);
INSERT INTO trigram_state (one, seq) VALUES (1, 0);`

// ensureTrigram creates the trigram index when it, or its position, is
// missing; catchUp then builds it from the current memories in the same
// transaction. A table without a position was left by a wenna that kept the
// index in the versioned schema, or by one that crashed mid-create: what it
// holds is unknown, so it is replaced.
func ensureTrigram(ctx context.Context, tx *sql.Tx) error {
	var ok bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'memory_trigram')
			AND EXISTS (SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'trigram_state')`).Scan(&ok); err != nil {
		return fmt.Errorf("sqlitestore: trigram index: check: %w", err)
	}
	if ok {
		// The table exists, so the subquery compiles; a missing row is no
		// position.
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM trigram_state)").Scan(&ok); err != nil {
			return fmt.Errorf("sqlitestore: trigram index: check position: %w", err)
		}
	}
	if ok {
		return nil
	}
	for _, q := range []string{"DROP TABLE IF EXISTS memory_trigram", "DROP TABLE IF EXISTS trigram_state", trigramSchema} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("sqlitestore: trigram index: create: %w", err)
		}
	}
	return nil
}

// catchUpTrigram brings the trigram index to the word index's position. It
// runs after the word index's catch-up, in the same transaction, and derives
// its rows from memories rather than replaying events: memories holds the
// rowid each memory has now, while a memory forgotten since the trigram
// index's position no longer has one, and its rowid may since belong to
// another memory.
//
// Every memory changed after the position has its row replaced, which also
// covers a reused rowid: its new memory changed after the position. Rows of
// memories forgotten after it, which no memory holds, are deleted; that scans
// the index's rowids, so it runs only when such a forget exists.
func catchUpTrigram(ctx context.Context, tx *sql.Tx) error {
	var done, upTo int64
	if err := tx.QueryRowContext(ctx, "SELECT (SELECT seq FROM trigram_state), (SELECT seq FROM index_state)").Scan(&done, &upTo); err != nil {
		return fmt.Errorf("sqlitestore: trigram index: read state: %w", err)
	}
	if done == upTo {
		return nil
	}
	var forgot bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM memory_events WHERE seq > ? AND kind = 'forget')", done).Scan(&forgot); err != nil {
		return fmt.Errorf("sqlitestore: trigram index: read forgets: %w", err)
	}
	if forgot {
		orphans, err := int64s(ctx, tx, "SELECT rowid FROM memory_trigram WHERE rowid NOT IN (SELECT id FROM memories)")
		if err != nil {
			return fmt.Errorf("sqlitestore: trigram index: read orphans: %w", err)
		}
		for _, rowid := range orphans {
			if _, err := tx.ExecContext(ctx, "DELETE FROM memory_trigram WHERE rowid = ?", rowid); err != nil {
				return fmt.Errorf("sqlitestore: trigram index: delete orphan: %w", err)
			}
		}
	}
	changed, err := changedSince(ctx, tx, done)
	if err != nil {
		return fmt.Errorf("sqlitestore: trigram index: read changes: %w", err)
	}
	for _, r := range changed {
		if _, err := tx.ExecContext(ctx, "DELETE FROM memory_trigram WHERE rowid = ?", r.id); err != nil {
			return fmt.Errorf("sqlitestore: trigram index: memory %d: %w", r.id, err)
		}
		if err := insertFTS(ctx, tx, "memory_trigram", r.id, r.content, r.keywords); err != nil {
			return fmt.Errorf("sqlitestore: trigram index: memory %d: %w", r.id, err)
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE trigram_state SET seq = ?", upTo); err != nil {
		return fmt.Errorf("sqlitestore: trigram index: write state: %w", err)
	}
	return nil
}

// indexedMemory is a current memory's rowid and latest text.
type indexedMemory struct {
	id       int64
	content  sql.NullString
	keywords sql.NullString
}

// changedSince reads every current memory whose latest event is after seq,
// in full before any is written.
func changedSince(ctx context.Context, tx *sql.Tx, seq int64) (out []indexedMemory, err error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT m.id, e.content, e.keywords FROM memories m
		JOIN memory_events e ON e.seq = m.seq
		WHERE m.seq > ?`, seq)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows, &err)
	for rows.Next() {
		var r indexedMemory
		if err := rows.Scan(&r.id, &r.content, &r.keywords); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// int64s reads a one-column query in full.
func int64s(ctx context.Context, tx *sql.Tx, query string) (out []int64, err error) {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows, &err)
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
