package sqlitestore

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/uni-industries/wenna/backend/internal/memory"
)

// stemRunes is how many leading letters of a query word must start a word of
// a memory for it to match ("preferences" finds "prefers"): models phrase
// queries in other forms of the words the user said. A shorter query word
// matches words it starts ("tea" finds "team").
const stemRunes = 5

// candidates is how many memories recall ranks before cutting to the limit;
// TimeWeightHigh reorders within them.
const candidates = 2 * memory.MaxLimit

// trigramRunes is the shortest query word the trigram index can match.
const trigramRunes = 3

// hitsSQL ranks the memories of full-text index FTS (memory_fts, or
// memory_trigram when on) matching ?1 (any query word): those holding the
// whole query as a phrase (?2) first, then by how many query words they hold
// in the indexes recall reads (MATCHED), then by
// bm25, where rarer words weigh more and a keyword weighs more than content.
// ?3 is the candidate cap. FTS, MATCHED and TIEBREAK are replaced with fixed
// SQL (hitsQuery).
const hitsSQL = `
	SELECT m.memory_id, e.content, e.keywords, e.created_at, m.seq, m.id,
		FTS.rowid IN (SELECT rowid FROM FTS WHERE FTS MATCH ?2) AS phrase,
		MATCHED AS matched
	FROM FTS
	JOIN memories m ON m.id = FTS.rowid
	JOIN memory_events e ON e.seq = m.seq
	WHERE FTS MATCH ?1
	ORDER BY phrase DESC, matched DESC, bm25(FTS, 1.0, 3.0), TIEBREAK
	LIMIT ?3`

// wordMatchedSQL is 1 when the word index holds a query word (its prefix
// expression, a numbered parameter); trigramMatchedSQL, ORed onto it, when
// the trigram index holds it inside a word.
const (
	wordMatchedSQL    = "FTS.rowid IN (SELECT rowid FROM memory_fts WHERE memory_fts MATCH ?%d)"
	trigramMatchedSQL = " OR FTS.rowid IN (SELECT rowid FROM memory_trigram WHERE memory_trigram MATCH ?%d)"
)

// term is one distinct query word as matchExprs builds it: its word-index
// expression; its trigram-index expression, "" when the trigram index is off
// or the word is shorter than trigramRunes; and the word-index expressions of
// the adjacent-word joins it is part of (matchExprs), any of which also
// counts it as held.
type term struct {
	word, trigram string
	joined        []string
}

// hitsQuery is hitsSQL over table for terms and a tie-break. Term
// expressions are parameters numbered ?4 on, in hits' argument order.
func hitsQuery(table string, terms []term, tieBreak string) string {
	matched := make([]string, len(terms))
	n := 4
	for i, t := range terms {
		matched[i] = fmt.Sprintf(wordMatchedSQL, n)
		n++
		if t.trigram != "" {
			matched[i] += fmt.Sprintf(trigramMatchedSQL, n)
			n++
		}
		for range t.joined {
			matched[i] += " OR " + fmt.Sprintf(wordMatchedSQL, n)
			n++
		}
		matched[i] = "(" + matched[i] + ")"
	}
	q := strings.NewReplacer("MATCHED", strings.Join(matched, " + "), "TIEBREAK", tieBreak).Replace(hitsSQL)
	// A second pass: a Replacer does not rescan what it put in.
	return strings.ReplaceAll(q, "FTS", table)
}

// hit is one recalled memory with its latest event's seq, for recency.
type hit struct {
	m   memory.Memory
	seq int64
}

// match is a hit with how it ranked in a full-text index.
type match struct {
	hit
	id      int64 // the memory's rowid in the index tables
	phrase  bool
	matched int
}

// hits runs hitsQuery over table: words and phrase are its MATCH
// expressions, terms what counts as a matched query word; at most limit rows.
func (s *Store) hits(ctx context.Context, table string, words []string, phrase string, terms []term, limit int, tieBreak string) (ms []match, err error) {
	args := []any{strings.Join(words, " OR "), phrase, limit}
	for _, t := range terms {
		args = append(args, t.word)
		if t.trigram != "" {
			args = append(args, t.trigram)
		}
		for _, j := range t.joined {
			args = append(args, j)
		}
	}
	rows, err := s.db.QueryContext(ctx, hitsQuery(table, terms, tieBreak), args...)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows, &err)
	for rows.Next() {
		var x match
		if x.m, err = scanMemory(rows, &x.seq, &x.id, &x.phrase, &x.matched); err != nil {
			return nil, err
		}
		ms = append(ms, x)
	}
	return ms, rows.Err()
}

// tieBreaks are the fixed ORDER BY terms that settle equal relevance; m is
// the memories table.
func tieBreak(r memory.RecallRequest) string {
	switch {
	case r.TimeWeight == memory.TimeWeightOff:
		return "m.memory_id"
	case r.Order == memory.OrderOldest:
		return "m.seq ASC"
	}
	return "m.seq DESC"
}

// Recall returns the current memories matching any word of the query,
// case-insensitively, in this order of relevance: the best matches (those
// that rank first on the phrase and on how many query words they hold),
// then memories sharing a name with them (related.go), then the other
// matches; within each, by bm25. A memory holds a query word when a word of
// it starts with the word's stem, or with two adjacent query words joined, the
// word among them ("Atlas Foods" finds "atlasfoods.com": it holds both). With
// the trigram index on, it also holds
// one of trigramRunes or more letters that appears inside a word ("foods" in
// "atlasfoods.com"), and memories the word index does not return at all
// follow when it finds fewer than the limit, ranked the same way.
// TimeWeight says how recency counts (memory.RecallRequest).
//
// The query is split into words in Go and each becomes a quoted FTS5 string:
// no part of it is ever read as FTS5 syntax. A query with no words matches
// nothing.
func (s *Store) Recall(ctx context.Context, req memory.RecallRequest) ([]memory.Memory, error) {
	ms, _, err := s.RecallTotal(ctx, req)
	return ms, err
}

// RecallTotal is Recall, plus how many memories match as well as the best
// one (the same phrase match and number of query words), however few the
// limit returns: a caller can tell a complete list from the first page of a
// longer one. Weaker matches are not counted: "met at SaaStr" counts who was
// met at SaaStr, not every memory with "at" in it. When the best matches fill
// candidates, they are counted in the index; one that holds every query word
// but not the phrase is counted by the word index alone.
func (s *Store) RecallTotal(ctx context.Context, req memory.RecallRequest) (_ []memory.Memory, total int, _ error) {
	req, err := req.Validate()
	if err != nil {
		return nil, 0, err
	}
	terms, phrase := matchExprs(req.Query, s.trigram)
	if len(terms) == 0 {
		return nil, 0, nil
	}
	if err := s.catchUpIfBehind(ctx); err != nil {
		return nil, 0, fmt.Errorf("sqlitestore: recall: %w", err)
	}
	tb := tieBreak(req)
	words := make([]string, len(terms))
	for i, t := range terms {
		words[i] = t.word
	}
	ms, err := s.hits(ctx, "memory_fts", words, phrase, terms, candidates, tb)
	if err != nil {
		return nil, 0, fmt.Errorf("sqlitestore: recall: %w", err)
	}
	var tri []match
	if tw, tp := trigramExprs(req.Query); s.trigram && len(ms) < req.Limit && len(tw) > 0 {
		// The word index's hits come back here too: fetch enough past them.
		if tri, err = s.hits(ctx, "memory_trigram", tw, tp, terms, candidates+len(ms), tb); err != nil {
			return nil, 0, fmt.Errorf("sqlitestore: recall: trigram: %w", err)
		}
	}
	if len(ms) == 0 && len(tri) == 0 {
		return nil, 0, nil
	}
	nBest := min(1, len(ms))
	for nBest < len(ms) && ms[nBest].phrase == ms[0].phrase && ms[nBest].matched == ms[0].matched {
		nBest++
	}
	out := make([]hit, 0, len(ms))
	var direct, best []int64
	for i, x := range ms[:nBest] {
		// Names lead from the best matches that can be returned.
		if i < req.Limit {
			direct = append(direct, x.id)
		}
		best = append(best, x.id)
		out = append(out, x.hit)
	}
	var rel []hit
	// Related ranks after the best matches: when they fill the limit, only
	// TimeWeightHigh, which reorders the candidates, can reach it. It never
	// returns a best match again, those past the limit included.
	if nBest < req.Limit || req.TimeWeight == memory.TimeWeightHigh {
		if rel, err = related(ctx, s.db, direct, best, tb, candidates); err != nil {
			return nil, 0, fmt.Errorf("sqlitestore: recall: related: %w", err)
		}
	}
	out = append(out, rel...)
	for _, x := range ms[nBest:] {
		if !slices.ContainsFunc(rel, func(r hit) bool { return r.m.ID == x.m.ID }) {
			out = append(out, x.hit)
		}
	}
	// Trigram-only hits rank after every word-index hit.
	for _, x := range tri {
		if !slices.ContainsFunc(out, func(h hit) bool { return h.m.ID == x.m.ID }) {
			out = append(out, x.hit)
		}
	}
	total = nBest
	if nBest == candidates {
		// Every candidate is a best match: count them all in the index.
		var n int
		switch {
		case ms[0].phrase:
			n, err = s.countMatches(ctx, phrase)
		case ms[0].matched == len(terms):
			n, err = s.countMatches(ctx, strings.Join(words, " AND "))
		}
		if err != nil {
			return nil, 0, fmt.Errorf("sqlitestore: recall: count: %w", err)
		}
		total = max(total, n)
	}
	out = out[:min(candidates, len(out))]
	if req.TimeWeight == memory.TimeWeightHigh {
		byRecency(out, req.Order)
	}
	res := make([]memory.Memory, 0, min(req.Limit, len(out)))
	for _, h := range out[:min(req.Limit, len(out))] {
		res = append(res, h.m)
	}
	return res, total, nil
}

// countMatches is how many memories the word index matches for expr, an
// FTS5 expression built from matchExprs.
func (s *Store) countMatches(ctx context.Context, expr string) (n int, err error) {
	err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM memory_fts WHERE memory_fts MATCH ?", expr).Scan(&n)
	return n, err
}

// byRecency reorders hits, given in relevance order, by relevance and
// recency equally: final = 0.5·relevance + 0.5·recency, each its rank scaled
// to [0, 1]. With n hits, a hit at relevance rank i and recency rank j scores
// 1 − (i+j)/(2(n−1)), so sorting by i+j is the same order. Ties keep
// relevance order.
func byRecency(hits []hit, order string) {
	n := len(hits)
	byTime := make([]int, n) // indexes into hits, most recent (or oldest) first
	for i := range byTime {
		byTime[i] = i
	}
	slices.SortFunc(byTime, func(a, b int) int {
		if order == memory.OrderOldest {
			return cmp.Compare(hits[a].seq, hits[b].seq)
		}
		return cmp.Compare(hits[b].seq, hits[a].seq)
	})
	key := make(map[string]int, n)
	for j, i := range byTime {
		key[hits[i].m.ID] = i + j
	}
	slices.SortStableFunc(hits, func(a, b hit) int { return cmp.Compare(key[a.m.ID], key[b.m.ID]) })
}

// maxTerms is the most distinct query words, and adjacent-word joins, recall
// matches on: each costs the index a lookup per candidate. Words come first:
// past maxTerms of them, the longest are kept (ties: the earliest), the rest
// dropped; the phrase is still the whole query. Joins fill the slots words
// leave, in query order (matchExprs).
const maxTerms = 8

// keptWords returns the distinct words of all, compared case-insensitively,
// that are at least minRunes long: the maxTerms longest (ties: the earliest),
// in query order.
func keptWords(all []string, minRunes int) []string {
	type word struct {
		w     string
		runes int
	}
	var ws []word
	seen := map[string]bool{}
	for _, w := range all {
		n := utf8.RuneCountInString(w)
		if k := strings.ToLower(w); n >= minRunes && !seen[k] {
			seen[k] = true
			ws = append(ws, word{w, n})
		}
	}
	if len(ws) > maxTerms {
		byLen := slices.Clone(ws)
		slices.SortStableFunc(byLen, func(a, b word) int { return cmp.Compare(b.runes, a.runes) })
		keep := map[string]bool{}
		for _, x := range byLen[:maxTerms] {
			keep[x.w] = true
		}
		ws = slices.DeleteFunc(ws, func(x word) bool { return !keep[x.w] })
	}
	out := make([]string, len(ws))
	for i, x := range ws {
		out[i] = x.w
	}
	return out
}

// matchExprs builds the FTS5 expressions for query: a term per distinct word
// (at most maxTerms of them, keptWords), and the whole query as a phrase.
// Each word is one quoted FTS5 string, so quotes, operators (AND, OR, NOT,
// NEAR), column filters, parentheses and * in the query are text, never
// syntax. In the word index a query word matches as a prefix of its first
// stemRunes letters; words sharing that prefix are one term, whose trigram
// expression, when trigram is on, ORs those of them with trigramRunes or
// more letters, each matching anywhere in the text. terms is empty when
// query has no words.
//
// Each two adjacent query words of different terms, joined, are one more
// word-index expression, matching as a prefix ("atlas foods" finds
// "atlasfoods"), that counts as holding both terms: a memory holding a term
// both ways counts it once. Joins take the maxTerms slots words leave, the
// earliest pair first, a repeated join once. They are not in the any-word
// MATCH: a join starts with its first word's stem, so it matches only
// memories that word does. They have no trigram expression: the trigram
// index already finds each word inside the join.
func matchExprs(query string, trigram bool) (terms []term, phrase string) {
	all := splitWords(query)
	group := map[string]int{} // lower-case prefix → index in terms
	for _, w := range keptWords(all, 1) {
		// A repeated word would count twice.
		p := prefix(w)
		i, ok := group[strings.ToLower(p)]
		if !ok {
			i = len(terms)
			group[strings.ToLower(p)] = i
			terms = append(terms, term{word: ftsString(p) + "*"})
		}
		if trigram && utf8.RuneCountInString(w) >= trigramRunes {
			if terms[i].trigram != "" {
				terms[i].trigram += " OR "
			}
			terms[i].trigram += ftsString(w)
		}
	}
	slots := maxTerms - len(terms)
	joins := map[string]bool{}
	for k := 1; k < len(all) && len(joins) < slots; k++ {
		a, aok := group[strings.ToLower(prefix(all[k-1]))]
		b, bok := group[strings.ToLower(prefix(all[k]))]
		j := strings.ToLower(all[k-1] + all[k])
		// A word the cap dropped joins nothing; joining a term to itself
		// counts nothing new.
		if !aok || !bok || a == b || joins[j] {
			continue
		}
		joins[j] = true
		expr := ftsString(j) + "*"
		terms[a].joined = append(terms[a].joined, expr)
		terms[b].joined = append(terms[b].joined, expr)
	}
	return terms, ftsString(strings.Join(all, " "))
}

// trigramExprs builds the trigram index's expressions for query, quoted as
// matchExprs does: one per distinct word of at least trigramRunes letters (at
// most maxTerms of them, keptWords), matching it anywhere in the text, and
// the whole query as a phrase. words is empty when no query word is that
// long.
func trigramExprs(query string) (words []string, phrase string) {
	all := splitWords(query)
	for _, w := range keptWords(all, trigramRunes) {
		words = append(words, ftsString(w))
	}
	return words, ftsString(strings.Join(all, " "))
}

// ftsString quotes s as an FTS5 string, doubling any quote inside.
func ftsString(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// prefix returns w's first stemRunes runes, or w if it is shorter.
func prefix(w string) string {
	n := 0
	for i := range w {
		if n == stemRunes {
			return w[:i]
		}
		n++
	}
	return w
}
