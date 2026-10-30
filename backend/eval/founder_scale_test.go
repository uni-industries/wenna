//go:build eval

package eval

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/uni-industries/wenna/backend/internal/memory"
	"github.com/uni-industries/wenna/backend/internal/sqlitestore"
)

// scaleTask is one founder request against the memory of scaleCorpus. check
// judges the answer and the memory afterwards, and returns what is wrong, or
// "".
type scaleTask struct {
	name, say, want string // want: the right answer, for the log
	check           func(t *testing.T, answer, db string) string
}

var sarahRE = regexp.MustCompile(`Sarah [A-Z][a-z]+`)

// roleOf parses a scaleCorpus role memory, "<name> is <role> at <company>.".
func roleOf(content string) (name, company string) {
	name, rest, _ := strings.Cut(content, " is ")
	at := strings.LastIndex(rest, " at ")
	return name, strings.TrimSuffix(rest[at+len(" at "):], ".")
}

// scaleTasks builds the requests from the corpus, so each has a known answer.
func scaleTasks(keys []string, memories map[string]string) []scaleTask {
	var roles []string
	byCompany := map[string][]string{}
	for _, k := range keys {
		if strings.HasPrefix(k, "p") && strings.HasSuffix(k, "/role") { // a contact's, not base/role
			roles = append(roles, k)
			_, co := roleOf(memories[k])
			byCompany[co] = append(byCompany[co], k)
		}
	}
	companies := make([]string, 0, len(byCompany))
	for co := range byCompany {
		companies = append(companies, co)
	}
	slices.Sort(companies)

	// A company with exactly one contact, and that contact's email.
	var soleCo, soleEmail string
	for _, co := range companies {
		if len(byCompany[co]) == 1 {
			p := strings.TrimSuffix(byCompany[co][0], "/role")
			_, email, _ := strings.Cut(memories[p+"/email"], "email is ")
			soleCo, soleEmail = co, strings.TrimSuffix(email, ".")
			break
		}
	}
	metSaaStr := 0
	var soc2 []string
	sarahs := map[string]bool{}
	for _, k := range keys {
		switch v := memories[k]; {
		case strings.HasSuffix(k, "/met") && strings.Contains(v, "at SaaStr"):
			metSaaStr++
		case strings.HasSuffix(k, "/promise") && strings.Contains(v, "a SOC 2 report"):
			name, _, _ := strings.Cut(strings.TrimPrefix(v, "Promised "), " a SOC 2 report")
			soc2 = append(soc2, name)
		case strings.HasPrefix(k, "p") && strings.HasSuffix(k, "/role") && strings.HasPrefix(v, "Sarah "):
			name, _ := roleOf(v)
			sarahs[strings.TrimPrefix(name, "Sarah ")] = true
		}
	}
	// A contact no base memory names: "Priya Natarajan" is also the cofounder.
	var mover, oldCo string
	for _, k := range roles {
		name, co := roleOf(memories[k])
		if !slices.ContainsFunc(keys, func(b string) bool {
			return strings.HasPrefix(b, "base/") && strings.Contains(memories[b], name)
		}) {
			mover, oldCo = name, co
			break
		}
	}

	return []scaleTask{
		{
			name: "contact email",
			say:  "What's the email address of our contact at " + soleCo + "?",
			want: soleEmail,
			check: func(_ *testing.T, answer, _ string) string {
				if !strings.Contains(strings.ToLower(answer), soleEmail) {
					return "answer lacks " + soleEmail
				}
				return ""
			},
		},
		{
			name: "count met at event",
			say:  "How many people did I meet at SaaStr? Give me the number.",
			want: strconv.Itoa(metSaaStr),
			check: func(_ *testing.T, answer, _ string) string {
				if !strings.Contains(answer, strconv.Itoa(metSaaStr)) {
					return fmt.Sprintf("answer lacks the count %d", metSaaStr)
				}
				return ""
			},
		},
		{
			name: "list everyone promised",
			say:  "List everyone I've promised a SOC 2 report to.",
			want: fmt.Sprintf("%d people: %s", len(soc2), strings.Join(soc2, ", ")),
			check: func(_ *testing.T, answer, _ string) string {
				found := 0
				for _, n := range soc2 {
					if strings.Contains(answer, n) {
						found++
					}
				}
				if found*10 < len(soc2)*9 {
					return fmt.Sprintf("answer names %d of the %d people", found, len(soc2))
				}
				return ""
			},
		},
		{
			name: "contact moved company",
			say:  mover + " has left " + oldCo + " and is now CTO at Quill Robotics.",
			want: "no current memory places " + mover + " at " + oldCo,
			check: func(t *testing.T, _, db string) string {
				var bad []string
				ms := recallAll(t, db, mover)
				if !slices.ContainsFunc(ms, func(m memory.Memory) bool { return strings.Contains(m.Content, "Quill") }) {
					bad = append(bad, "no memory places "+mover+" at Quill Robotics")
				}
				// The old company may stay as history ("his old Summit Energy
				// email"); a memory stating it as current is stale.
				past := []string{"left", "old", "former", "previous", "was "}
				for _, m := range ms {
					c := m.Content
					if strings.Contains(c, mover) && strings.Contains(c, oldCo) && !strings.Contains(c, "Quill") && !containsAny(strings.ToLower(c), past) {
						bad = append(bad, fmt.Sprintf("memory %q is stale", c))
					}
				}
				return strings.Join(bad, "; ")
			},
		},
		{
			name: "ambiguous first name",
			say:  "Draft a short email to Sarah about the pilot.",
			want: fmt.Sprintf("asks which of %d Sarahs", len(sarahs)),
			check: func(_ *testing.T, answer, _ string) string {
				// Contacts and candidates both hold Sarahs: any two named, or
				// a question, is asking.
				named := map[string]bool{}
				for _, m := range sarahRE.FindAllString(answer, -1) {
					named[m] = true
				}
				if len(named) < 2 && !strings.Contains(strings.ToLower(answer), "which") {
					return fmt.Sprintf("answer picks a Sarah without asking which of the %d", len(sarahs))
				}
				return ""
			},
		},
	}
}

// recallAll is every current memory recall finds for query, up to MaxLimit.
func recallAll(t *testing.T, db, query string) []memory.Memory {
	t.Helper()
	ctx := context.Background()
	store, err := sqlitestore.Open(ctx, db, sqlitestore.WithTrigram(true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()
	ms, err := store.Recall(ctx, memory.RecallRequest{Query: query, Limit: memory.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

// seed saves the corpus to a new database at db, and returns the ids saved.
func seed(t *testing.T, db string, keys []string, memories map[string]string) map[string]bool {
	t.Helper()
	ctx := context.Background()
	store, err := sqlitestore.Open(ctx, db, sqlitestore.WithTrigram(true))
	if err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]bool, len(keys))
	for _, k := range keys {
		m, err := store.Remember(ctx, memories[k], nil)
		if err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
		ids[m.ID] = true
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return ids
}

// TestFounderAtScale: each app, in a fresh session on a memory already
// holding scaleContacts people, does each scaleTask.
func TestFounderAtScale(t *testing.T) {
	bin := buildWenna(t)
	keys, memories, _ := scaleCorpus(loadRecallFixture(t).Memories)
	tasks := scaleTasks(keys, memories)
	for _, task := range tasks {
		t.Logf("%s: %q, want %s", task.name, task.say, task.want)
	}
	for run := range *runs {
		for _, task := range tasks {
			for _, a := range apps {
				name := fmt.Sprintf("run%d-%s-%s", run+1, strings.ReplaceAll(task.name, " ", "-"), a.name)
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					db := filepath.Join(t.TempDir(), "wenna.db")
					seed(t, db, keys, memories)
					work := t.TempDir()
					if a.stableDir {
						work = stableDir(t, name, a.name)
					}
					ctx, cancel := context.WithTimeout(context.Background(), stepTimeout)
					defer cancel()
					answer, err := a.ask(ctx, session{work: work, scratch: t.TempDir(), bin: bin, db: db}, task.say)
					if err != nil {
						t.Fatalf("%s: %v", a.name, err)
					}
					t.Logf("user: %s\napp:  %s", task.say, answer)
					if bad := task.check(t, answer, db); bad != "" {
						t.Errorf("%s: %s", a.name, bad)
					}
				})
			}
		}
	}
}
