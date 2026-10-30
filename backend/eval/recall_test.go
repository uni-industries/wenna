package eval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/uni-industries/wenna/backend/internal/memory"
	"github.com/uni-industries/wenna/backend/internal/sqlitestore"
)

// recallTopK is how many results a query may return and still count as a
// hit: what a model reads before it gives up on a recall.
const recallTopK = 5

// recallFloor is the hits TestRecallQuality last measured on
// testdata/recall.json. Raise it when recall improves; it may never fall.
const recallFloor = 35

type recallFixture struct {
	Memories map[string]string `json:"memories"`
	Queries  []struct {
		Query string   `json:"query"`
		Want  []string `json:"want"`
	} `json:"queries"`
}

func loadRecallFixture(t *testing.T) recallFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "recall.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx recallFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	return fx
}

// TestRecallQuality: a founder's memories, and the keyword queries models
// send for them. A query hits when any memory it wants is in the top
// recallTopK results. Misses are logged: they are where recall falls short.
func TestRecallQuality(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fx := loadRecallFixture(t)

	store, err := sqlitestore.Open(ctx, filepath.Join(t.TempDir(), "wenna.db"), sqlitestore.WithTrigram(true))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	// Sorted, so recency ties break the same way every run.
	keys := make([]string, 0, len(fx.Memories))
	for k := range fx.Memories {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	keyOf := map[string]string{}
	for _, k := range keys {
		m, err := store.Remember(ctx, fx.Memories[k], nil)
		if err != nil {
			t.Fatalf("remember %s: %v", k, err)
		}
		keyOf[m.ID] = k
	}

	hits := 0
	for _, q := range fx.Queries {
		for _, w := range q.Want {
			if _, ok := fx.Memories[w]; !ok {
				t.Fatalf("query %q wants unknown memory %q", q.Query, w)
			}
		}
		got, err := store.Recall(ctx, memory.RecallRequest{Query: q.Query, Limit: recallTopK})
		if err != nil {
			t.Fatalf("recall %q: %v", q.Query, err)
		}
		var gotKeys []string
		for _, m := range got {
			gotKeys = append(gotKeys, keyOf[m.ID])
		}
		if slices.ContainsFunc(q.Want, func(w string) bool { return slices.Contains(gotKeys, w) }) {
			hits++
		} else {
			t.Logf("miss: %q wants %v, got %v", q.Query, q.Want, gotKeys)
		}
	}
	t.Logf("recall: %d/%d queries hit in the top %d", hits, len(fx.Queries), recallTopK)
	if hits < recallFloor {
		t.Errorf("recall fell to %d hits, below the floor of %d", hits, recallFloor)
	}
}
