package eval

import (
	"context"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/uni-industries/wenna/backend/internal/memory"
	"github.com/uni-industries/wenna/backend/internal/sqlitestore"
)

// scaleContacts is how many people the founder's memory holds in
// TestRecallAtScale.
const scaleContacts = 1000

var (
	firstNames = strings.Fields(`Dana Sam Priya Miguel Sarah Wei Omar Lena Jonas Aisha Marco Yuki Elena Tom Grace
		Ravi Chloe Diego Nora Felix Hana Ivan Maya Leo Zara Ben Ines Kofi Mia Arjun Lucia Noah Amara Erik Sofia
		Hugo Tara Kenji Rosa Liam Nadia Owen Freya Malik Julia Pablo Anya Theo Iris Samir`)
	lastNames = strings.Fields(`Whitfield Chen Natarajan Ortega Kim Novak Haddad Larsen Okafor Rossi Tanaka Petrov
		Brennan Mehta Duarte Lindqvist Moreau Adeyemi Fischer Silva Nakamura Kowalski Reyes Bauer Costa Ibrahim
		Sato Walsh Kumar Jensen Alvarez Dubois Mensah Weber Castillo Ahmed Park Murphy Varga Lopez Schmidt Gupta
		Hoffman Yilmaz Romero Berg Nguyen Patel Becker`)
	companyFirst = strings.Fields(`Acme Northwind Harbor Granite Bluefin Copper Summit Cedar Atlas Lumina Vector
		Orbit Pioneer Redwood Silverline Tidal Quartz Meridian Falcon Keystone Juniper Beacon Cobalt Evergreen Polar`)
	companySecond = strings.Fields(`Robotics Freight Logistics Health Analytics Foods Energy Systems Capital Labs Retail Motors`)
	roles         = []string{"VP of Operations", "CTO", "CEO", "Head of Data", "COO", "Director of Logistics", "Head of Procurement", "CFO", "VP of Engineering", "Product Manager"}
	events        = []string{"SaaStr", "Web Summit", "a YC dinner", "ProMat", "Manifest", "a Harbor Ventures offsite", "an Austin meetup"}
	topics        = []string{"pricing", "the pilot results", "an integration with their ERP", "renewal terms", "SOC 2 questions", "a referral", "hiring"}
	promises      = []string{"a SOC 2 report", "a pricing proposal", "an intro to Priya", "a demo of forecasting", "the case study draft"}
	stages        = []string{"discovery", "pilot", "negotiation", "closed won", "closed lost"}
	months        = []string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October"}
	hireRoles     = []string{"account executive", "senior engineer", "designer", "data scientist"}
	hireStages    = []string{"phone screen", "onsite", "offer"}
)

type scaleQuery struct {
	kind, query string
	want        []string // keys; a hit is any of them
}

// scaleCorpus is a founder's memory at scale, built the same way every run:
// memories by key, in the order they are saved, and queries with answers.
func scaleCorpus(base map[string]string) (keys []string, memories map[string]string, queries []scaleQuery) {
	r := rand.New(rand.NewPCG(1, 2))
	memories = map[string]string{}
	add := func(key, content string) {
		keys = append(keys, key)
		memories[key] = content
	}
	baseKeys := make([]string, 0, len(base))
	for k := range base {
		baseKeys = append(baseKeys, k)
	}
	slices.Sort(baseKeys)
	for _, k := range baseKeys {
		add("base/"+k, base[k])
	}

	type company struct{ name, domain, kind string }
	var companies []company
	for _, a := range companyFirst {
		for _, b := range companySecond {
			kind := []string{"customer", "prospect", "vendor", "partner"}[r.IntN(4)]
			if b == "Capital" {
				kind = "investor"
			}
			companies = append(companies, company{a + " " + b, strings.ToLower(a+b) + ".com", kind})
		}
	}
	for i, c := range companies {
		switch c.kind {
		case "customer":
			add(fmt.Sprintf("co%d/rel", i), fmt.Sprintf("%s is a Lumen Labs customer with %d seats, renewing in %s 2027.", c.name, 5+r.IntN(200), months[r.IntN(len(months))]))
		case "prospect":
			add(fmt.Sprintf("co%d/rel", i), fmt.Sprintf("The %s deal is in %s, worth $%dk a year.", c.name, stages[r.IntN(len(stages))], 10+r.IntN(300)))
		case "investor":
			add(fmt.Sprintf("co%d/rel", i), fmt.Sprintf("%s is an investor in Lumen Labs, with a $%dk check in the pre-seed.", c.name, 25*(1+r.IntN(20))))
		default:
			add(fmt.Sprintf("co%d/rel", i), fmt.Sprintf("%s is a Lumen Labs %s.", c.name, c.kind))
		}
	}

	type person struct {
		first, last string
		co          int
	}
	seen := map[string]bool{}
	var people []person
	for len(people) < scaleContacts {
		p := person{firstNames[r.IntN(len(firstNames))], lastNames[r.IntN(len(lastNames))], r.IntN(len(companies))}
		if seen[p.first+p.last] {
			continue
		}
		seen[p.first+p.last] = true
		people = append(people, p)
	}
	byCompany := map[int][]int{}
	for i, p := range people {
		c := companies[p.co]
		name := p.first + " " + p.last
		pk := fmt.Sprintf("p%d", i)
		byCompany[p.co] = append(byCompany[p.co], i)
		add(pk+"/role", fmt.Sprintf("%s is %s at %s.", name, roles[r.IntN(len(roles))], c.name))
		add(pk+"/email", fmt.Sprintf("%s's email is %s.%s@%s.", name, strings.ToLower(p.first), strings.ToLower(p.last), c.domain))
		if r.IntN(2) == 0 {
			add(pk+"/met", fmt.Sprintf("Met %s at %s in %s 2026.", name, events[r.IntN(len(events))], months[r.IntN(len(months))]))
		}
		if r.IntN(2) == 0 {
			add(pk+"/spoke", fmt.Sprintf("Last spoke with %s on %s %d about %s.", name, months[r.IntN(len(months))], 1+r.IntN(28), topics[r.IntN(len(topics))]))
		}
		if r.IntN(5) == 0 {
			add(pk+"/promise", fmt.Sprintf("Promised %s %s by %s %d.", name, promises[r.IntN(len(promises))], months[r.IntN(len(months))], 1+r.IntN(28)))
		}
	}
	for i := range 40 {
		first, last := firstNames[r.IntN(len(firstNames))], lastNames[r.IntN(len(lastNames))]
		role := hireRoles[r.IntN(len(hireRoles))]
		add(fmt.Sprintf("cand%d", i), fmt.Sprintf("%s %s is a candidate for %s, at the %s stage.", first, last, role, hireStages[r.IntN(len(hireStages))]))
	}
	for i, m := range months {
		add("mrr/"+m, fmt.Sprintf("Lumen Labs MRR was $%dk in %s 2026.", 20+2*i+r.IntN(3), m))
	}

	// Queries, phrased as the keywords a model sends.
	for _, i := range r.Perm(len(people))[:100] {
		p, pk := people[i], fmt.Sprintf("p%d", i)
		name := p.first + " " + p.last
		queries = append(queries,
			scaleQuery{"person by name", name, keysWithPrefix(keys, pk+"/")},
			scaleQuery{"person's email", name + " email", []string{pk + "/email"}},
			scaleQuery{"first name at company", p.first + " " + companies[p.co].name, []string{pk + "/role"}},
		)
		if _, ok := memories[pk+"/spoke"]; ok {
			queries = append(queries, scaleQuery{"last conversation", "last spoke " + name, []string{pk + "/spoke"}})
		}
		if _, ok := memories[pk+"/promise"]; ok {
			queries = append(queries, scaleQuery{"promise to a person", "promised " + p.first + " " + p.last, []string{pk + "/promise"}})
		}
	}
	for _, ci := range r.Perm(len(companies))[:60] {
		c := companies[ci]
		var roleKeys, emailKeys []string
		for _, i := range byCompany[ci] {
			roleKeys = append(roleKeys, fmt.Sprintf("p%d/role", i))
			emailKeys = append(emailKeys, fmt.Sprintf("p%d/email", i))
		}
		queries = append(queries, scaleQuery{"company relationship", c.name, []string{fmt.Sprintf("co%d/rel", ci)}})
		if len(roleKeys) > 0 {
			queries = append(queries,
				scaleQuery{"contact at company", c.name + " contact", roleKeys},
				scaleQuery{"email of contact at company (two-step)", c.name + " contact email", emailKeys},
			)
		}
	}
	for _, m := range months {
		queries = append(queries, scaleQuery{"metric for a month", "MRR " + m, []string{"mrr/" + m}})
	}
	for _, role := range hireRoles {
		var want []string
		for k, v := range memories {
			if strings.HasPrefix(k, "cand") && strings.Contains(v, "candidate for "+role) {
				want = append(want, k)
			}
		}
		queries = append(queries, scaleQuery{"candidates for a role", role + " candidates", want})
	}
	return keys, memories, queries
}

// scaleListQueries are questions whose answer is many memories, at
// scaleContacts people: everyone matching, not one needle.
func scaleListQueries(keys []string, memories map[string]string) []scaleQuery {
	where := func(prefix, substr string) []string {
		var out []string
		for _, k := range keys {
			if strings.Contains(k, prefix) && strings.Contains(memories[k], substr) {
				out = append(out, k)
			}
		}
		return out
	}
	return []scaleQuery{
		{"everyone met at an event", "met at SaaStr", where("/met", "SaaStr")},
		{"all investors", "investors", where("/rel", "is an investor")},
		{"deals at a stage", "deals in negotiation", where("/rel", "is in negotiation")},
		{"all open promises", "promised", where("/promise", "Promised")},
		{"customers renewing in a month", "customers renewing March", where("/rel", "renewing in March")},
		{"everyone sharing a first name", "Sarah email", where("/email", "Sarah ")},
		{"everyone in a role", "CTO", where("/role", " is CTO at")},
	}
}

func keysWithPrefix(keys []string, prefix string) []string {
	var out []string
	for _, k := range keys {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out
}

// TestRecallAtScale: a founder's memory with scaleContacts people, the
// companies they work at, and the deals, promises, hires and metrics around
// them. Logs, per kind of query, how often recall finds a right memory in the
// top 5 and top memory.DefaultLimit, and how long a recall takes.
func TestRecallAtScale(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := loadRecallFixture(t).Memories
	keys, memories, queries := scaleCorpus(base)

	store, err := sqlitestore.Open(ctx, filepath.Join(t.TempDir(), "wenna.db"), sqlitestore.WithTrigram(true))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	start := time.Now()
	keyOf := map[string]string{}
	for _, k := range keys {
		m, err := store.Remember(ctx, memories[k], nil)
		if err != nil {
			t.Fatalf("remember %s: %v", k, err)
		}
		keyOf[m.ID] = k
	}
	t.Logf("saved %d memories in %v", len(keys), time.Since(start).Round(time.Millisecond))

	type tally struct{ n, top5, topDefault int }
	byKind := map[string]*tally{}
	var kinds []string
	var took []time.Duration
	for _, q := range queries {
		if byKind[q.kind] == nil {
			byKind[q.kind] = &tally{}
			kinds = append(kinds, q.kind)
		}
		start := time.Now()
		got, err := store.Recall(ctx, memory.RecallRequest{Query: q.query, Limit: memory.DefaultLimit})
		took = append(took, time.Since(start))
		if err != nil {
			t.Fatalf("recall %q: %v", q.query, err)
		}
		rank := slices.IndexFunc(got, func(m memory.Memory) bool { return slices.Contains(q.want, keyOf[m.ID]) })
		tl := byKind[q.kind]
		tl.n++
		if rank >= 0 && rank < 5 {
			tl.top5++
		}
		if rank >= 0 {
			tl.topDefault++
		} else if tl.n-tl.topDefault <= 2 {
			var gotKeys []string
			for _, m := range got[:min(5, len(got))] {
				gotKeys = append(gotKeys, keyOf[m.ID])
			}
			t.Logf("miss (%s): %q wants %v, top 5 %v", q.kind, q.query, q.want[:min(3, len(q.want))], gotKeys)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-40s %5s %7s %7s\n", "query kind", "n", "top 5", "top 20")
	for _, k := range kinds {
		tl := byKind[k]
		fmt.Fprintf(&b, "%-40s %5d %6.0f%% %6.0f%%\n", k, tl.n, 100*float64(tl.top5)/float64(tl.n), 100*float64(tl.topDefault)/float64(tl.n))
	}
	fmt.Fprintf(&b, "\n%-40s %5s %9s %9s %9s\n", "list question", "want", "found", "of top 20", "total")
	for _, q := range scaleListQueries(keys, memories) {
		got, err := store.Recall(ctx, memory.RecallRequest{Query: q.query, Limit: memory.MaxLimit})
		if err != nil {
			t.Fatalf("recall %q: %v", q.query, err)
		}
		// What an app sees on its first, default-limit recall.
		first, total, err := store.RecallTotal(ctx, memory.RecallRequest{Query: q.query})
		if err != nil {
			t.Fatalf("recall %q: %v", q.query, err)
		}
		if len(q.want) > len(first) && total <= len(first) {
			t.Errorf("%q: %d memories answer it, the first recall returned %d and a total of %d: the list looks complete", q.query, len(q.want), len(first), total)
		}
		found, inTop := 0, 0
		for i, m := range got {
			if slices.Contains(q.want, keyOf[m.ID]) {
				found++
				if i < memory.DefaultLimit {
					inTop++
				}
			}
		}
		fmt.Fprintf(&b, "%-40s %5d %8.0f%% %9d %9d\n", q.kind+": "+q.query, len(q.want), 100*float64(found)/float64(max(1, len(q.want))), inTop, total)
	}
	slices.Sort(took)
	fmt.Fprintf(&b, "\nrecall over %d memories: p50 %v, p95 %v, max %v", len(keys),
		took[len(took)/2].Round(time.Microsecond), took[len(took)*95/100].Round(time.Microsecond), took[len(took)-1].Round(time.Microsecond))
	t.Log("\n" + b.String())
}
