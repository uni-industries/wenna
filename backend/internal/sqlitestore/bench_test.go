package sqlitestore

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/uni-industries/wenna/backend/internal/id"
	"github.com/uni-industries/wenna/backend/internal/memory"
)

// benchMemories is the store size the recall latency target is set at.
const benchMemories = 30_000

// benchModes runs bench with the trigram index off, then on.
func benchModes(b *testing.B, bench func(b *testing.B, trigram bool)) {
	for _, on := range []bool{false, true} {
		b.Run(fmt.Sprintf("trigram=%v", on), func(b *testing.B) { bench(b, on) })
	}
}

// benchStore fills a store with n synthetic memories, written as history and
// indexed by catchUp in one transaction, as a migration would.
func benchStore(b *testing.B, n int, trigram bool) (*Store, []string) {
	b.Helper()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(b.TempDir(), "wenna.db"), WithTrigram(trigram))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := s.Close(); err != nil {
			b.Errorf("Close: %v", err)
		}
	})
	r := rand.New(rand.NewPCG(1, 2))
	// Random lower-case words, 3 to 10 letters; benchVocab's first entries
	// are the most frequent (Zipf).
	vocab := make([]string, 5000)
	for i := range vocab {
		w := make([]byte, 3+r.IntN(8))
		for j := range w {
			w[j] = byte('a' + r.IntN(26))
		}
		vocab[i] = string(w)
	}
	names := make([]string, 500)
	for i := range names {
		names[i] = fmt.Sprintf("Name%d", i)
	}
	zipf := rand.NewZipf(r, 1.1, 1, uint64(len(vocab)-1))
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		for i := range n {
			words := make([]string, 6+r.IntN(20))
			for j := range words {
				words[j] = vocab[zipf.Uint64()]
			}
			// Mid-sentence names, so related has work to do.
			words[1] = names[r.IntN(len(names))]
			words[len(words)-1] = names[r.IntN(len(names))]
			if _, err := tx.ExecContext(ctx, "INSERT INTO memory_events (memory_id, kind, content, created_at) VALUES (?, 'remember', ?, ?)",
				id.New(), "The "+strings.Join(words, " "), int64(i)); err != nil {
				return err
			}
		}
		return s.catchUp(ctx, tx)
	})
	if err != nil {
		b.Fatal(err)
	}
	return s, vocab
}

// BenchmarkRecall30K reports recall latency at benchMemories memories over a
// mix of queries: rare and common words, a name, phrases, a long query.
// p50-ms is the median over every recall run.
func BenchmarkRecall30K(b *testing.B) { benchModes(b, benchRecall30K) }

func benchRecall30K(b *testing.B, trigram bool) {
	s, v := benchStore(b, benchMemories, trigram)
	ctx := context.Background()
	// Inside the commonest word of five or more letters: no word starts with
	// it, so the trigram fallback, when on, ranks most memories.
	infix := ""
	for _, w := range v {
		if len(w) >= 5 {
			infix = w[1:]
			break
		}
	}
	queries := []memory.RecallRequest{
		{Query: infix},                                // inside a common word: the trigram fallback
		{Query: v[3000]},                              // rare
		{Query: v[0]},                                 // in most memories
		{Query: "Name42"},                             // a name
		{Query: v[0] + " " + v[1] + " " + v[2]},       // common words
		{Query: v[40] + " " + v[700] + " " + v[2500]}, // a phrase that is not there
		{Query: "Name7 " + v[40], TimeWeight: memory.TimeWeightHigh},
		{Query: "the first customer contact for Name7 " + v[0] + " " + v[40] + " " + v[300] + " " + v[4000]},
		{Query: "zzzz qqqq"}, // nothing matches
	}
	// The longest query of the most distinct words: two-letter words, each
	// the start of many memory words.
	var short []string
	for a := 'a'; a <= 'z' && len(short) < memory.MaxQueryRunes/3; a++ {
		for c := 'a'; c <= 'z' && len(short) < memory.MaxQueryRunes/3; c++ {
			short = append(short, string([]rune{a, c}))
		}
	}
	queries = append(queries,
		memory.RecallRequest{Query: strings.Join(short, " ")},
		memory.RecallRequest{Query: strings.Join(v[:40], " ")}, // the commonest words
	)
	var took []time.Duration
	i := 0
	for b.Loop() {
		q := queries[i%len(queries)]
		i++
		start := time.Now()
		if _, err := s.Recall(ctx, q); err != nil {
			b.Fatal(err)
		}
		took = append(took, time.Since(start))
	}
	for qi, q := range queries {
		var per []time.Duration
		for j := qi; j < len(took); j += len(queries) {
			per = append(per, took[j])
		}
		slices.Sort(per)
		if len(per) > 0 {
			b.Logf("%q %s: p50 %s", q.Query, q.TimeWeight, per[len(per)/2])
		}
	}
	slices.Sort(took)
	b.ReportMetric(float64(took[len(took)/2].Microseconds())/1000, "p50-ms")
	b.ReportMetric(float64(took[len(took)*9/10].Microseconds())/1000, "p90-ms")
	var size int64
	if err := s.db.QueryRowContext(ctx, "SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()").Scan(&size); err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(float64(size)/(1<<20), "db-MiB")
}

// BenchmarkRemember30K reports one new remember at benchMemories memories.
func BenchmarkRemember30K(b *testing.B) { benchModes(b, benchRemember30K) }

func benchRemember30K(b *testing.B, trigram bool) {
	s, _ := benchStore(b, benchMemories, trigram)
	ctx := context.Background()
	for b.Loop() {
		if _, err := s.Remember(ctx, "fresh fact "+id.New(), nil); err != nil {
			b.Fatal(err)
		}
	}
}
