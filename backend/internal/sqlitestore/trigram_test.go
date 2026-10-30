package sqlitestore

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"testing"

	"github.com/uni-industries/wenna/backend/internal/memory"
)

// trigramTables lists the trigram index's tables in db: memory_trigram, its
// FTS5 shadow tables and trigram_state.
func trigramTables(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master
		WHERE name LIKE 'memory\_trigram%' ESCAPE '\' OR name = 'trigram_state' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	return names
}

// trigramSeq is the trigram index's position.
func trigramSeq(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var seq int64
	if err := db.QueryRow("SELECT seq FROM trigram_state").Scan(&seq); err != nil {
		t.Fatal(err)
	}
	return seq
}

// TestTrigramOffNeverCreates: with the index off, nothing of it exists after
// every kind of write and a recall; on, it exists from Open.
func TestTrigramOffNeverCreates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)
	m := remember(t, s, "Ann Lee's email is ann.lee@atlasfoods.com.")
	if _, err := s.Update(ctx, m.ID, "Ann Lee's email is ann@bigfoods.com.", nil); err != nil {
		t.Fatal(err)
	}
	remember(t, s, "Shop list is on the fridge.")
	if _, err := s.Forget(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if got := recall(t, s, memory.RecallRequest{Query: "foods shop"}); got != "Shop list is on the fridge." {
		t.Errorf("recall = %q", got)
	}
	if got := trigramTables(t, s.db); len(got) != 0 {
		t.Errorf("trigram tables with the index off: %q", got)
	}
	checkIndex(t, s)

	// Control: the query sees the tables when they exist.
	on := newStore(t, WithTrigram(true))
	if got := trigramTables(t, on.db); len(got) < 2 {
		t.Errorf("trigram tables with the index on: %q, want the table and its state", got)
	}
}

// TestTrigramToggle: a store with the index off leaves an existing one as it
// is; turned on again, the index catches up with every write made while off.
func TestTrigramToggle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "wenna.db")
	on := open(t, path, WithTrigram(true))
	kept := remember(t, on, "Ordered from bigfoods.")
	gone := remember(t, on, "Ordered from megashop.")
	seq := trigramSeq(t, on.db)
	if err := on.Close(); err != nil {
		t.Fatal(err)
	}

	off := open(t, path)
	added := remember(t, off, "Ann Lee's email is ann.lee@atlasfoods.com.")
	if _, err := off.Update(ctx, kept.ID, "Ordered from smallfoods.", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := off.Forget(ctx, gone.ID); err != nil {
		t.Fatal(err)
	}
	if got := recall(t, off, memory.RecallRequest{Query: "foods"}); got != "" {
		t.Errorf("off: recall(foods) = %q, want nothing: the word index alone", got)
	}
	if got := trigramSeq(t, off.db); got != seq {
		t.Errorf("off moved the trigram position: %d, was %d", got, seq)
	}
	var rows int
	if err := off.db.QueryRow("SELECT count(*) FROM memory_trigram").Scan(&rows); err != nil || rows != 2 {
		t.Errorf("off changed the trigram index: %d rows, %v; want the 2 written on", rows, err)
	}
	if err := off.Close(); err != nil {
		t.Fatal(err)
	}

	again := open(t, path, WithTrigram(true))
	checkIndex(t, again)
	recallSet(t, again, "foods", added.Content, "Ordered from smallfoods.")
	if got := recall(t, again, memory.RecallRequest{Query: "egashop"}); got != "" {
		t.Errorf("on again: recall(egashop) = %q, want the forgotten memory gone", got)
	}
	if got := recall(t, again, memory.RecallRequest{Query: "bigfoods"}); got != "" {
		t.Errorf("on again: recall(bigfoods) = %q, want the old text gone", got)
	}
}

// recallSet asserts recall(query) returns exactly the memories want, in any
// order.
func recallSet(t *testing.T, s *Store, query string, want ...string) {
	t.Helper()
	got, err := s.Recall(context.Background(), memory.RecallRequest{Query: query})
	if err != nil {
		t.Fatal(err)
	}
	g, w := ids(got), slices.Clone(want)
	slices.Sort(g)
	slices.Sort(w)
	if !slices.Equal(g, w) {
		t.Errorf("recall(%s) = %q, want %q in any order", query, ids(got), want)
	}
}

// TestTrigramRebuildsUnknownState: turning the index on rebuilds it from the
// current memories when its position is missing, whatever the table holds:
// a table left by a wenna that created it in the versioned schema, an empty
// position, or a position without its table.
func TestTrigramRebuildsUnknownState(t *testing.T) {
	t.Parallel()
	const oldTable = `CREATE VIRTUAL TABLE memory_trigram USING fts5(
		content, keywords, content = '', contentless_delete = 1, tokenize = 'trigram')`
	for name, damage := range map[string][]string{
		"table without position": {
			oldTable,
			"INSERT INTO memory_trigram (rowid, content, keywords) VALUES (1, 'stale garbage text', ''), (999, 'orphan garbage text', '')",
		},
		"empty position": {
			oldTable,
			"INSERT INTO memory_trigram (rowid, content, keywords) VALUES (1, 'stale garbage text', '')",
			"CREATE TABLE trigram_state (one INTEGER PRIMARY KEY CHECK (one = 1), seq INTEGER NOT NULL)",
		},
		"position without table": {
			"CREATE TABLE trigram_state (one INTEGER PRIMARY KEY CHECK (one = 1), seq INTEGER NOT NULL)",
			"INSERT INTO trigram_state (one, seq) VALUES (1, 1)",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "wenna.db")
			off := open(t, path)
			remember(t, off, "Ann Lee's email is ann.lee@atlasfoods.com.")
			remember(t, off, "Ordered from megashop.")
			for _, q := range damage {
				if _, err := off.db.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			if err := off.Close(); err != nil {
				t.Fatal(err)
			}
			on := open(t, path, WithTrigram(true))
			checkIndex(t, on)
			if got := recall(t, on, memory.RecallRequest{Query: "garbage"}); got != "" {
				t.Errorf("recall(garbage) = %q, want the stale rows gone", got)
			}
			if got := recall(t, on, memory.RecallRequest{Query: "foods"}); got != "Ann Lee's email is ann.lee@atlasfoods.com." {
				t.Errorf("recall(foods) = %q, want the email", got)
			}
		})
	}
}

// TestTrigramMixedStores: two stores on one file, one with the index on and
// one off, write in turn. The off store's writes reach the on store's index
// before its next recall, including a forget whose rowid a later memory
// takes.
func TestTrigramMixedStores(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "wenna.db")
	on, off := open(t, path, WithTrigram(true)), open(t, path)
	rowid := func(id string) int64 {
		t.Helper()
		var r int64
		if err := on.db.QueryRow("SELECT id FROM memories WHERE memory_id = ?", id).Scan(&r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	a := remember(t, on, "Ordered from bigfoods.")
	b := remember(t, off, "Ordered from megashop.")
	if got := recall(t, on, memory.RecallRequest{Query: "egashop"}); got != b.Content {
		t.Errorf("recall(egashop) = %q, want the off store's memory", got)
	}
	// The off store forgets the newest memory, then saves one that takes its
	// rowid: the on store must not find the old text at that rowid.
	bRowid := rowid(b.ID)
	if _, err := off.Forget(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	c := remember(t, off, "Lunch at tinycafe.")
	if got := rowid(c.ID); got != bRowid {
		t.Fatalf("rowid %d not reused (%d): the case below would test nothing", bRowid, got)
	}
	if got := recall(t, on, memory.RecallRequest{Query: "egashop"}); got != "" {
		t.Errorf("recall(egashop) = %q, want nothing: forgotten by the off store", got)
	}
	if got := recall(t, on, memory.RecallRequest{Query: "nycafe"}); got != c.Content {
		t.Errorf("recall(nycafe) = %q, want the reused rowid's memory", got)
	}
	checkIndex(t, on)
	// Interleave: on writes, off updates, on forgets, off writes.
	d := remember(t, on, "Ordered from hugefoods.")
	if _, err := off.Update(ctx, a.ID, "Ordered from smallfoods.", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := on.Forget(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	e := remember(t, off, "Dinner at bigdiner.")
	if got := recall(t, on, memory.RecallRequest{Query: "foods"}); got != "Ordered from smallfoods." {
		t.Errorf("recall(foods) = %q, want the off store's update only", got)
	}
	if got := recall(t, on, memory.RecallRequest{Query: "diner"}); got != e.Content {
		t.Errorf("recall(diner) = %q", got)
	}
	checkIndex(t, on)
	checkIndex(t, off)
	if got := recall(t, off, memory.RecallRequest{Query: "foods"}); got != "" {
		t.Errorf("off: recall(foods) = %q, want nothing", got)
	}
}
