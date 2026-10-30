package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/text/cases"
)

// The index tables (schema_v2.sql, and trigram.go's when on) are derived from
// memory_events. Every write applies its own event in its transaction through
// catchUp, the one path that changes them. catchUp applies every event the
// index has not seen, so it also builds the index on migration.

type event struct {
	seq      int64
	memoryID string
	kind     string
	content  sql.NullString
	keywords sql.NullString
}

// catchUp applies, in order, every event after the last one the index
// reflects, then, when s has the trigram index on, brings it to the same
// position. tx must be a write transaction.
func (s *Store) catchUp(ctx context.Context, tx *sql.Tx) error {
	if err := catchUpWords(ctx, tx); err != nil {
		return err
	}
	if s.trigram {
		return catchUpTrigram(ctx, tx)
	}
	return nil
}

// catchUpWords brings the word index (memories, memory_fts, memory_words)
// up to date with the history.
func catchUpWords(ctx context.Context, tx *sql.Tx) (err error) {
	var done int64
	if err := tx.QueryRowContext(ctx, "SELECT seq FROM index_state").Scan(&done); err != nil {
		return fmt.Errorf("sqlitestore: index: read state: %w", err)
	}
	evs, err := eventsAfter(ctx, tx, done)
	if err != nil {
		return fmt.Errorf("sqlitestore: index: read events: %w", err)
	}
	if len(evs) == 0 {
		return nil
	}
	fold := cases.Fold() // not safe for concurrent use: one per call
	for _, e := range evs {
		if err := apply(ctx, tx, fold, e); err != nil {
			return fmt.Errorf("sqlitestore: index: event %d: %w", e.seq, err)
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE index_state SET seq = ?", evs[len(evs)-1].seq); err != nil {
		return fmt.Errorf("sqlitestore: index: write state: %w", err)
	}
	return nil
}

// eventsAfter reads every event after seq, in order. They are read in full
// before any is applied, so no statement writes while rows are open.
func eventsAfter(ctx context.Context, tx *sql.Tx, seq int64) (evs []event, err error) {
	rows, err := tx.QueryContext(ctx, "SELECT seq, memory_id, kind, content, keywords FROM memory_events WHERE seq > ? ORDER BY seq", seq)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows, &err)
	for rows.Next() {
		var e event
		if err := rows.Scan(&e.seq, &e.memoryID, &e.kind, &e.content, &e.keywords); err != nil {
			return nil, err
		}
		evs = append(evs, e)
	}
	return evs, rows.Err()
}

// apply makes the index reflect one event: a remember replaces the memory's
// rows, a forget deletes them.
func apply(ctx context.Context, tx *sql.Tx, fold cases.Caser, e event) error {
	var rowid int64
	err := tx.QueryRowContext(ctx, "SELECT id FROM memories WHERE memory_id = ?", e.memoryID).Scan(&rowid)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if exists {
		if err := unindex(ctx, tx, rowid); err != nil {
			return err
		}
	}
	if e.kind != "remember" {
		if exists {
			_, err := tx.ExecContext(ctx, "DELETE FROM memories WHERE id = ?", rowid)
			return err
		}
		return nil
	}
	if exists {
		if _, err := tx.ExecContext(ctx, "UPDATE memories SET seq = ? WHERE id = ?", e.seq, rowid); err != nil {
			return err
		}
	} else {
		res, err := tx.ExecContext(ctx, "INSERT INTO memories (memory_id, seq) VALUES (?, ?)", e.memoryID, e.seq)
		if err != nil {
			return err
		}
		if rowid, err = res.LastInsertId(); err != nil {
			return err
		}
	}
	if err := insertFTS(ctx, tx, "memory_fts", rowid, e.content, e.keywords); err != nil {
		return err
	}
	for word, flags := range wordFlags(fold, e.content.String) {
		if _, err := tx.ExecContext(ctx, "INSERT INTO memory_words (word, id, flags) VALUES (?, ?, ?)", word, rowid, flags); err != nil {
			return err
		}
	}
	return nil
}

// insertFTS adds a memory's row to full-text index table: memory_fts or
// memory_trigram, a fixed name, never input. Keywords are one per line.
func insertFTS(ctx context.Context, tx *sql.Tx, table string, rowid int64, content, keywords sql.NullString) error {
	ks, err := decodeKeywords(keywords)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO "+table+" (rowid, content, keywords) VALUES (?, ?, ?)",
		rowid, content.String, strings.Join(ks, "\n"))
	return err
}

// unindex deletes a memory's word-index rows.
func unindex(ctx context.Context, tx *sql.Tx, rowid int64) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM memory_fts WHERE rowid = ?", rowid); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM memory_words WHERE id = ?", rowid)
	return err
}

// Whether an index is behind the history: one read each. The trigram form
// names trigram_state, so only a store with the index on runs it.
const (
	behindSQL        = "SELECT coalesce((SELECT max(seq) FROM memory_events), 0) > (SELECT seq FROM index_state)"
	behindTrigramSQL = behindSQL + " OR coalesce((SELECT max(seq) FROM memory_events), 0) > (SELECT seq FROM trigram_state)"
)

// catchUpIfBehind brings the index up to date when an event it has not seen
// exists: a write by another wenna that does not keep it, an older one or one
// with the trigram index off.
func (s *Store) catchUpIfBehind(ctx context.Context) error {
	q := behindSQL
	if s.trigram {
		q = behindTrigramSQL
	}
	var behind bool
	if err := s.db.QueryRowContext(ctx, q).Scan(&behind); err != nil {
		return fmt.Errorf("sqlitestore: index: check: %w", err)
	}
	if !behind {
		return nil
	}
	return s.inTx(ctx, func(tx *sql.Tx) error { return s.catchUp(ctx, tx) })
}
