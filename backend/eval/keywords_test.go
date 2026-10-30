//go:build eval

package eval

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/uni-industries/wenna/backend/internal/memory"
	"github.com/uni-industries/wenna/backend/internal/sqlitestore"
)

// synonymFacts are facts a user states in one set of words and later asks
// for in another: the misses of testdata/recall.json. Only the keywords an
// app saves with the fact can bridge them.
var synonymFacts = []struct {
	say, query string
}{
	{"We charge $49 per seat per month.", "pricing"},
	{"We raised a $2M pre-seed led by Harbor Ventures.", "investors"},
	{"We lost two customers in Q3.", "churn"},
	{"Each cofounder owns 45% of the company.", "equity split"},
	{"We're a Delaware C-corp.", "incorporation"},
	{"Our outside counsel is Brightline Law.", "lawyer"},
	{"We started our SOC 2 Type I audit in October.", "security compliance"},
	{"I'm on vacation from December 20 to January 3.", "time off holiday"},
	{"We have 7 employees.", "team size"},
	{"We run production on AWS in us-east-1.", "cloud hosting provider"},
}

// appMemories is the current content and keywords, by id, of the memories
// in store not in seeded: those the app saved.
func appMemories(ctx context.Context, store *sqlitestore.Store, seeded map[string]bool) (map[string]string, error) {
	out := map[string]string{}
	err := store.Export(ctx, func(e memory.Event) error {
		switch {
		case seeded[e.MemoryID]:
		case e.Content == nil:
			delete(out, e.MemoryID)
		default:
			out[e.MemoryID] = fmt.Sprintf("%q %q", *e.Content, e.Keywords)
		}
		return nil
	})
	return out, err
}

// TestKeywordsBridgeSynonyms: each app, in a fresh session on a memory of
// scaleContacts people, hears one fact; the question's words, which the
// fact lacks, must then recall it in the top 5.
func TestKeywordsBridgeSynonyms(t *testing.T) {
	bin := buildWenna(t)
	// The founder's own facts left out: each test states its one.
	keys, memories, _ := scaleCorpus(nil)
	for run := range *runs {
		for _, f := range synonymFacts {
			for _, a := range apps {
				name := fmt.Sprintf("run%d-%s-%s", run+1, strings.ReplaceAll(f.query, " ", "-"), a.name)
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					db := filepath.Join(t.TempDir(), "wenna.db")
					seeded := seed(t, db, keys, memories)
					work := t.TempDir()
					if a.stableDir {
						work = stableDir(t, name, a.name)
					}
					ctx, cancel := context.WithTimeout(context.Background(), stepTimeout)
					defer cancel()
					if _, err := a.ask(ctx, session{work: work, scratch: t.TempDir(), bin: bin, db: db}, f.say); err != nil {
						t.Fatalf("%s: %v", a.name, err)
					}
					store, err := sqlitestore.Open(ctx, db, sqlitestore.WithTrigram(true))
					if err != nil {
						t.Fatal(err)
					}
					defer func() {
						if err := store.Close(); err != nil {
							t.Error(err)
						}
					}()
					saved, err := appMemories(ctx, store, seeded)
					if err != nil {
						t.Fatal(err)
					}
					t.Logf("%s saved: %v", a.name, saved)
					if len(saved) == 0 {
						t.Fatalf("%s: saved nothing", a.name)
					}
					got, err := store.Recall(ctx, memory.RecallRequest{Query: f.query, Limit: 5})
					if err != nil {
						t.Fatal(err)
					}
					if !slices.ContainsFunc(got, func(m memory.Memory) bool { return saved[m.ID] != "" }) {
						t.Errorf("%s: recall(%q) top 5 lacks the fact", a.name, f.query)
					}
				})
			}
		}
	}
}
