package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"golang.org/x/text/cases"

	"github.com/uni-industries/wenna/backend/internal/memory"
)

func recall(t *testing.T, s *Store, req memory.RecallRequest) string {
	t.Helper()
	got, err := s.Recall(context.Background(), req)
	if err != nil {
		t.Fatalf("recall(%+v): %v", req, err)
	}
	return strings.Join(ids(got), "|")
}

func remember(t *testing.T, s *Store, content string, keywords ...string) memory.Memory {
	t.Helper()
	m, err := s.Remember(context.Background(), content, keywords)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// checkIndex asserts the index tables s keeps hold exactly the current
// memories, each at its latest event and text, and are caught up with the
// history: the word index always, the trigram index when s has it on.
func checkIndex(t *testing.T, s *Store) {
	t.Helper()
	db := s.db
	var bad int
	err := db.QueryRow(`
		SELECT
			(SELECT count(*) FROM (SELECT memory_id, seq FROM (` + current + `) WHERE kind = 'remember'
				EXCEPT SELECT memory_id, seq FROM memories))
			+ (SELECT count(*) FROM (SELECT memory_id, seq FROM memories
				EXCEPT SELECT memory_id, seq FROM (` + current + `) WHERE kind = 'remember'))
			+ (SELECT count(*) FROM memory_words WHERE id NOT IN (SELECT id FROM memories))
			+ (SELECT coalesce(max(seq), 0) FROM memory_events) - (SELECT seq FROM index_state)`).Scan(&bad)
	if err != nil || bad != 0 {
		t.Errorf("index differs from history: %d, %v", bad, err)
	}
	tables := []string{"memory_fts"}
	if s.trigram {
		tables = append(tables, "memory_trigram")
		var behind int
		if err := db.QueryRow("SELECT (SELECT seq FROM index_state) - (SELECT seq FROM trigram_state)").Scan(&behind); err != nil || behind != 0 {
			t.Errorf("trigram index behind the word index by %d, %v", behind, err)
		}
	}
	for _, table := range tables {
		var fts, mem int
		if err := db.QueryRow("SELECT (SELECT count(*) FROM "+table+"), (SELECT count(*) FROM memories)").Scan(&fts, &mem); err != nil || fts != mem {
			t.Errorf("%s rows = %d, memories = %d, %v", table, fts, mem, err)
		}
		checkFTSContents(t, db, table)
	}
	// Each memory's words are exactly those of its current content.
	rows, err := db.Query(`
		SELECT m.id, e.content, coalesce((SELECT group_concat(word || '=' || flags, ' ')
			FROM (SELECT word, flags FROM memory_words w WHERE w.id = m.id ORDER BY word)), '')
		FROM memories m JOIN memory_events e ON e.seq = m.seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Error(err)
		}
	}()
	for rows.Next() {
		var rowid int64
		var content, got string
		if err := rows.Scan(&rowid, &content, &got); err != nil {
			t.Fatal(err)
		}
		var want []string
		for w, f := range wordFlags(cases.Fold(), content) {
			want = append(want, fmt.Sprintf("%s=%d", w, f))
		}
		slices.Sort(want)
		if w := strings.Join(want, " "); got != w {
			t.Errorf("memory %d words = %q, want %q", rowid, got, w)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

// checkFTSContents asserts full-text index table holds, token for token,
// what indexing each current memory afresh gives: a fresh table from table's
// own definition, compared through fts5vocab, which reads a contentless
// index. Runs in a transaction it rolls back, which drops the temp tables.
func checkFTSContents(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil {
			t.Error(err)
		}
	}()
	var def string
	if err := tx.QueryRow("SELECT sql FROM sqlite_master WHERE name = ?", table).Scan(&def); err != nil {
		t.Fatalf("%s: definition: %v", table, err)
	}
	fresh := "temp.fresh_" + table
	create, ok := strings.CutPrefix(def, "CREATE VIRTUAL TABLE "+table+" ")
	if !ok {
		t.Fatalf("%s: unexpected definition %q", table, def)
	}
	for _, q := range []string{
		"CREATE VIRTUAL TABLE " + fresh + " " + create,
		"CREATE VIRTUAL TABLE temp.have_vocab USING fts5vocab(main, " + table + ", instance)",
		"CREATE VIRTUAL TABLE temp.want_vocab USING fts5vocab(temp, fresh_" + table + ", instance)",
	} {
		if _, err := tx.Exec(q); err != nil {
			t.Fatalf("%s: %s: %v", table, q, err)
		}
	}
	rows, err := tx.Query("SELECT m.id, e.content, e.keywords FROM memories m JOIN memory_events e ON e.seq = m.seq")
	if err != nil {
		t.Fatal(err)
	}
	var mems []indexedMemory
	for rows.Next() {
		var r indexedMemory
		if err := rows.Scan(&r.id, &r.content, &r.keywords); err != nil {
			t.Fatal(err)
		}
		mems = append(mems, r)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatal(err)
	}
	for _, r := range mems {
		if err := insertFTS(ctx, tx, fresh, r.id, r.content, r.keywords); err != nil {
			t.Fatal(err)
		}
	}
	var missing, extra, tokens int
	if err := tx.QueryRow(`SELECT
		(SELECT count(*) FROM (SELECT * FROM temp.want_vocab EXCEPT SELECT * FROM temp.have_vocab)),
		(SELECT count(*) FROM (SELECT * FROM temp.have_vocab EXCEPT SELECT * FROM temp.want_vocab)),
		(SELECT count(*) FROM temp.want_vocab)`).Scan(&missing, &extra, &tokens); err != nil {
		t.Fatal(err)
	}
	if missing != 0 || extra != 0 {
		t.Errorf("%s: %d tokens missing, %d stale, of %d", table, missing, extra, tokens)
	}
	if len(mems) > 0 && tokens == 0 {
		t.Errorf("%s: no tokens for %d memories: the comparison saw nothing", table, len(mems))
	}
}

// TestMatchExprs: every expression is quoted FTS5 strings joined by fixed
// syntax, whatever the query holds.
func TestMatchExprs(t *testing.T) {
	t.Parallel()
	exprs := func(q string) (words, tris string, phrase string) {
		terms, phrase := matchExprs(q, true)
		var ws, ts []string
		for _, tm := range terms {
			ws = append(ws, tm.word)
			ts = append(ts, tm.trigram)
		}
		return strings.Join(ws, " "), strings.Join(ts, "|"), phrase
	}
	// Two queries: each stays within maxTerms.
	words, tris, phrase := exprs(`a"b NEAR(c) d* "e" f:g`)
	if words != `"a"* "b"* "NEAR"* "c"* "d"* "e"* "f"* "g"*` {
		t.Errorf("words = %s", words)
	}
	if tris != `||"NEAR"|||||` {
		t.Errorf("trigrams = %s", tris)
	}
	if phrase != `"a b NEAR c d e f g"` {
		t.Errorf("phrase = %s", phrase)
	}
	// A term's trigram expression ORs its distinct whole words of three or
	// more letters; shorter words have none.
	if words, tris, _ := exprs(`Allergies allergic ALLERGIC Ok"x Bob`); words != `"Aller"* "Ok"* "x"* "Bob"*` || tris != `"Allergies" OR "allergic"|||"Bob"` {
		t.Errorf("words = %s, trigrams = %s", words, tris)
	}
	// Past maxTerms distinct words, the longest are kept (ties: the
	// earliest), in query order; the phrase is the whole query.
	long := `aa bb cc dd ee ff gg hh ii Banana kiwi KIWI ll`
	if maxTerms != 8 {
		t.Fatalf("maxTerms = %d: rewrite this case", maxTerms)
	}
	words, tris, phrase = exprs(long)
	if words != `"aa"* "bb"* "cc"* "dd"* "ee"* "ff"* "Banan"* "kiwi"*` || tris != `||||||"Banana"|"kiwi"` {
		t.Errorf("capped: words = %s, trigrams = %s", words, tris)
	}
	if phrase != ftsString(long) {
		t.Errorf("capped: phrase = %s", phrase)
	}
	if tw, _ := trigramExprs(`abc abcd abcde abcdef abcdefg abcdefgh abcdefghi abcdefghij abcdefghijk xyz`); strings.Join(tw, " ") !=
		`"abcd" "abcde" "abcdef" "abcdefg" "abcdefgh" "abcdefghi" "abcdefghij" "abcdefghijk"` {
		t.Errorf("capped trigram words = %s", tw)
	}
	if got := ftsString(`x"y`); got != `"x""y"` {
		t.Errorf("ftsString = %s, want the quote doubled", got)
	}
	if terms, _ := matchExprs(`"!* ()`, true); len(terms) != 0 {
		t.Errorf("terms = %q, want none", terms)
	}
	// With the trigram index off, the same words and no trigram expression.
	off, offPhrase := matchExprs(`Allergies allergic ALLERGIC Ok"x Bob`, false)
	on, onPhrase := matchExprs(`Allergies allergic ALLERGIC Ok"x Bob`, true)
	if len(off) != len(on) || offPhrase != onPhrase {
		t.Fatalf("off = %q %s, on = %q %s", off, offPhrase, on, onPhrase)
	}
	for i := range off {
		if off[i].word != on[i].word || off[i].trigram != "" || !slices.Equal(off[i].joined, on[i].joined) {
			t.Errorf("off term %d = %q, want word %s, joins %q and no trigram", i, off[i], on[i].word, on[i].joined)
		}
	}

	// Joins: two adjacent words of different terms, lower-cased, as one
	// quoted prefix, listed under both terms; only in the slots words leave,
	// earliest first, each once.
	joined := func(q string) string {
		terms, _ := matchExprs(q, true)
		var js []string
		for _, tm := range terms {
			js = append(js, strings.Join(tm.joined, ","))
		}
		return strings.Join(js, "|")
	}
	for _, tc := range []struct{ query, want string }{
		// Words of one term ("Allergies allergic") do not join.
		{`Allergies allergic ALLERGIC Ok"x Bob`, `"allergicok"*|"allergicok"*,"okx"*|"okx"*,"xbob"*|"xbob"*`},
		{"Atlas Foods contact email", `"atlasfoods"*|"atlasfoods"*,"foodscontact"*|"foodscontact"*,"contactemail"*|"contactemail"*`},
		// Five words leave three slots: syntax is text, quoted; "nearz" is
		// past the cap.
		{`x" OR "y NEAR(z)`, `"xor"*|"xor"*,"ory"*|"ory"*,"ynear"*|"ynear"*|`},
		{`a"" b`, `"ab"*|"ab"*`},
		// A repeated join counts once.
		{"a b a b", `"ab"*,"ba"*|"ab"*,"ba"*`},
		// Seven words leave one slot; eight, none.
		{"a b c d e f g", `"ab"*|"ab"*|||||`},
		{"a b c d e f g h", `|||||||`},
		// A word the cap dropped (x, y: the shortest) joins nothing, before or
		// after: eight words, seven terms, one slot.
		{"aaaaab x bb y aaaaac cc dd ee ff gg", `"aaaaaccc"*||"aaaaaccc"*||||`},
		{"one", ``},
		{"", ``},
	} {
		if got := joined(tc.query); got != tc.want {
			t.Errorf("joins(%s) = %s, want %s", tc.query, got, tc.want)
		}
	}

	// Trigram terms: whole words of three or more letters, never prefixes.
	tw, phrase := trigramExprs(`a"bc NEAR(c) dd* "eee" f:ggg Foods FOODS atlasfoods ab`)
	if got := strings.Join(tw, " "); got != `"NEAR" "eee" "ggg" "Foods" "atlasfoods"` {
		t.Errorf("trigram words = %s", got)
	}
	if phrase != `"a bc NEAR c dd eee f ggg Foods FOODS atlasfoods ab"` {
		t.Errorf("trigram phrase = %s", phrase)
	}
	if words, _ := trigramExprs(`ab "c" de*`); len(words) != 0 {
		t.Errorf("trigram words = %q, want none: every word is under three letters", words)
	}
}

// either is on with the trigram index on, else off: a want that differs by
// mode.
func either(trigram bool, on, off string) string {
	if trigram {
		return on
	}
	return off
}

// TestRecallTrigram: text inside a word matches through the trigram index,
// after every word-index hit, and only when the word index finds fewer than
// the limit. With the index off, only the word index matches.
func TestRecallTrigram(t *testing.T) { eachMode(t, testRecallTrigram) }

func testRecallTrigram(t *testing.T, trigram bool) {
	ctx := context.Background()
	s := newStore(t, WithTrigram(trigram))
	email := remember(t, s, "Ann Lee's email is ann.lee@atlasfoods.com.")
	if got, want := recall(t, s, memory.RecallRequest{Query: "foods"}), either(trigram, email.Content, ""); got != want {
		t.Errorf("recall(foods) = %q, want %q", got, want)
	}
	if got := recall(t, s, memory.RecallRequest{Query: "ATLASFOODS.COM"}); got != email.Content {
		t.Errorf("recall(ATLASFOODS.COM) = %q, want the email: case-insensitive", got)
	}
	// Under three letters: no trigram, and "ee" starts no word.
	if got := recall(t, s, memory.RecallRequest{Query: "ee"}); got != "" {
		t.Errorf("recall(ee) = %q, want nothing", got)
	}

	// The trigram-only hit holds both query words, the word hit one; the word
	// hit still ranks first.
	both := remember(t, s, "Ordered from bigfoods and megashop.")
	one := remember(t, s, "Shop list is on the fridge.")
	if got, want := recall(t, s, memory.RecallRequest{Query: "foods shop"}), either(trigram, one.Content+"|"+both.Content+"|"+email.Content, one.Content); got != want {
		t.Errorf("recall(foods shop) = %q, want %q", got, want)
	}
	// Among trigram-only hits, more query words first.
	if got, want := recall(t, s, memory.RecallRequest{Query: "foods egas"}), either(trigram, both.Content+"|"+email.Content, ""); got != want {
		t.Errorf("recall(foods egas) = %q, want %q", got, want)
	}

	// The index follows update and forget.
	if _, err := s.Update(ctx, both.ID, "Ordered from elsewhere.", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Forget(ctx, email.ID); err != nil {
		t.Fatal(err)
	}
	if got := recall(t, s, memory.RecallRequest{Query: "foods"}); got != "" {
		t.Errorf("recall(foods) after update and forget = %q, want nothing", got)
	}
	if got := recall(t, s, memory.RecallRequest{Query: "elsewh"}); got != "Ordered from elsewhere." {
		t.Errorf("recall(elsewh) = %q, want the updated memory", got)
	}
	checkIndex(t, s)
}

// TestRecallTrigramCountsAsMatched: a query word the trigram index holds
// inside a word counts toward how many query words a word-index hit holds,
// lifting it above a hit holding fewer, whatever bm25 says. With the index
// off, it does not count, and bm25 decides.
func TestRecallTrigramCountsAsMatched(t *testing.T) { eachMode(t, testRecallTrigramCountsAsMatched) }

func testRecallTrigramCountsAsMatched(t *testing.T, trigram bool) {
	s := newStore(t, WithTrigram(trigram))
	// Holds "lee", "email" and "foods" (inside atlasfoods), long: weak bm25.
	three := remember(t, s, "Lee's email, for every order of the shared kitchen, is the support desk at atlasfoods.com.")
	// Holds "lee" and "email", short and repeated: strong bm25.
	two := remember(t, s, "Lee email. Lee email.")
	if got, want := recall(t, s, memory.RecallRequest{Query: "Lee email foods", TimeWeight: memory.TimeWeightOff}),
		either(trigram, three.Content+"|"+two.Content, two.Content+"|"+three.Content); got != want {
		t.Errorf("recall(Lee email foods) = %q, want %q", got, want)
	}
	// Control: holding as many query words, bm25 ranks the short one first.
	if got, want := recall(t, s, memory.RecallRequest{Query: "Lee email", TimeWeight: memory.TimeWeightOff}), two.Content+"|"+three.Content; got != want {
		t.Errorf("recall(Lee email) = %q, want %q", got, want)
	}

	// A word the word index alone holds, by its stem ("emailed" in the
	// trigram index is in neither), still counts.
	s = newStore(t, WithTrigram(trigram))
	stem := remember(t, s, "Lee sent the email to the whole kitchen team on Friday.")
	lee := remember(t, s, "Lee Lee Lee Lee.")
	if got, want := recall(t, s, memory.RecallRequest{Query: "Lee emailed", TimeWeight: memory.TimeWeightOff}), stem.Content+"|"+lee.Content; got != want {
		t.Errorf("recall(Lee emailed) = %q, want %q", got, want)
	}
	if got, want := recall(t, s, memory.RecallRequest{Query: "Lee", TimeWeight: memory.TimeWeightOff}), lee.Content+"|"+stem.Content; got != want {
		t.Errorf("recall(Lee) = %q, want %q: the control", got, want)
	}
}

// TestRecallJoinsAdjacentWords: two adjacent query words find the word they
// form, so the contact's email at "Atlas Foods" (atlasfoods.com) ranks first
// among emails at every other "Atlas" company and the role memories naming
// the company, in either mode.
func TestRecallJoinsAdjacentWords(t *testing.T) { eachMode(t, testRecallJoinsAdjacentWords) }

func testRecallJoinsAdjacentWords(t *testing.T, trigram bool) {
	s := newStore(t, WithTrigram(trigram))
	var want string
	for i, co := range []string{"Foods", "Bakery", "Motors", "Labs", "Systems", "Capital", "Freight", "Health"} {
		first := []string{"Dana", "Ann", "Raj", "Mia", "Leo", "Kim", "Sam", "Eva"}[i]
		remember(t, s, fmt.Sprintf("%s Lee is CTO at Atlas %s.", first, co))
		m := remember(t, s, fmt.Sprintf("%s Lee's email is %s.lee@atlas%s.com.", first, strings.ToLower(first), strings.ToLower(co)))
		if co == "Foods" {
			want = m.Content
		}
	}
	for _, q := range []string{"Atlas Foods contact email", "atlas foods email", "email Atlas Food"} {
		if got := recall(t, s, memory.RecallRequest{Query: q, Limit: 1}); got != want {
			t.Errorf("recall(%s) = %q, want %q", q, got, want)
		}
	}
}

// matchedOf is how many query words recall counts each memory holding any of
// query as holding, by content.
func matchedOf(t *testing.T, s *Store, query string) map[string]int {
	t.Helper()
	terms, phrase := matchExprs(query, s.trigram)
	words := make([]string, len(terms))
	for i, tm := range terms {
		words[i] = tm.word
	}
	ms, err := s.hits(context.Background(), "memory_fts", words, phrase, terms, candidates, "m.memory_id")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, m := range ms {
		out[m.m.Content] = m.matched
	}
	return out
}

// TestRecallJoinCountsBothWords: a memory holding the join of two query words
// holds both, once each, however else it holds them.
func TestRecallJoinCountsBothWords(t *testing.T) { eachMode(t, testRecallJoinCountsBothWords) }

func testRecallJoinCountsBothWords(t *testing.T, trigram bool) {
	s := newStore(t, WithTrigram(trigram))
	for _, c := range []string{"atlasfoods", "Atlas Foods atlasfoods", "Atlas", "atlasfoodsinc email", "foodsatlas"} {
		remember(t, s, c)
	}
	got := matchedOf(t, s, "Atlas Foods email")
	want := map[string]int{
		"atlasfoods":             2, // both words, by the join alone
		"Atlas Foods atlasfoods": 2, // both ways, counted once
		"Atlas":                  1,
		"atlasfoodsinc email":    3, // the join is a prefix
		"foodsatlas":             1, // only adjacent words in query order join; "foods" is inside a word
	}
	if trigram {
		want["foodsatlas"] = 2 // the trigram index finds "atlas" inside it
	}
	if !maps.Equal(got, want) {
		t.Errorf("matched = %v, want %v", got, want)
	}
}

// TestRecallTrigramOnlyBelowLimit: when the word index fills the limit, no
// trigram hit is a candidate, so TimeWeightHigh cannot pull one in.
func TestRecallTrigramOnlyBelowLimit(t *testing.T) { eachMode(t, testRecallTrigramOnlyBelowLimit) }

func testRecallTrigramOnlyBelowLimit(t *testing.T, trigram bool) {
	s := newStore(t, WithTrigram(trigram))
	older := remember(t, s, "Shop hours are nine to five.")
	newer := remember(t, s, "Shop list is on the fridge.")
	remember(t, s, "Ordered from megashop.") // trigram-only, newest
	req := memory.RecallRequest{Query: "shop", Limit: 2, TimeWeight: memory.TimeWeightHigh}
	if got, want := recall(t, s, req), newer.Content+"|"+older.Content; got != want {
		t.Errorf("recall(%+v) = %q, want %q", req, got, want)
	}
}

// TestRecallQueryIsNeverSyntax: FTS5 query syntax in a query is text. Each
// query must succeed and match by its words alone, in either mode.
func TestRecallQueryIsNeverSyntax(t *testing.T) { eachMode(t, testRecallQueryIsNeverSyntax) }

func testRecallQueryIsNeverSyntax(t *testing.T, trigram bool) {
	s := newStore(t, WithTrigram(trigram))
	for _, c := range []string{
		"Allergic to peanuts",
		`Wrote "NEAR" AND "OR" NOT content: in a note`,
		"Likes ^start and -minus signs",
	} {
		remember(t, s, c)
	}
	tests := []struct{ query, want string }{
		{`"`, ""},
		{`""`, ""},
		{`"peanuts`, "Allergic to peanuts"},
		{`peanuts"`, "Allergic to peanuts"},
		// OR is a word here: the note holds it.
		{`" OR "" OR peanuts`, "Allergic to peanuts|Wrote \"NEAR\" AND \"OR\" NOT content: in a note"},
		{"peanuts*", "Allergic to peanuts"},
		{"*", ""},
		{"pea*nuts", "Allergic to peanuts"},
		// Operators are words: NOT does not exclude, NEAR is matched as text.
		{"NOT peanuts", "Allergic to peanuts|Wrote \"NEAR\" AND \"OR\" NOT content: in a note"},
		{"NEAR(peanuts allergic, 2)", "Allergic to peanuts|Wrote \"NEAR\" AND \"OR\" NOT content: in a note"},
		{"near", `Wrote "NEAR" AND "OR" NOT content: in a note`},
		{"AND", `Likes ^start and -minus signs|Wrote "NEAR" AND "OR" NOT content: in a note`},
		{"OR", `Wrote "NEAR" AND "OR" NOT content: in a note`},
		// Column filters are words.
		{"content:peanuts", "Allergic to peanuts|Wrote \"NEAR\" AND \"OR\" NOT content: in a note"},
		{"keywords:peanuts", "Allergic to peanuts"},
		{"{content keywords}: peanuts", "Allergic to peanuts|Wrote \"NEAR\" AND \"OR\" NOT content: in a note"},
		{"(peanuts)", "Allergic to peanuts"},
		{"(peanuts", "Allergic to peanuts"},
		{"^peanuts", "Allergic to peanuts"},
		{"-peanuts", "Allergic to peanuts"},
		{"+peanuts", "Allergic to peanuts"},
		// Nothing left after splitting into words.
		{"!!! ... ???", ""},
		{"́́", ""},
		// A letter to Go that SQLite's tokenizer may not know.
		{"\U0001E4D0", ""},
		{"'); DROP TABLE memory_events;--", ""},
		{"'); DROP TABLE memory_trigram;--", ""},
	}
	const note = "Wrote \"NEAR\" AND \"OR\" NOT content: in a note"
	// The same through the trigram index: text inside a word. Off, "anuts"
	// matches nothing and the other words decide.
	for _, tc := range []struct{ query, on, off string }{
		{`"anuts`, "Allergic to peanuts", ""},
		{`anuts"`, "Allergic to peanuts", ""},
		{"anuts*", "Allergic to peanuts", ""},
		{"(anuts", "Allergic to peanuts", ""},
		{"-anuts", "Allergic to peanuts", ""},
		{"^anuts", "Allergic to peanuts", ""},
		{`anuts OR "`, note + "|Allergic to peanuts", note},
		{"NOT anuts", note + "|Allergic to peanuts", note},
		{"NEAR(anuts llergic, 2)", note + "|Allergic to peanuts", note},
		{"content:anuts", note + "|Allergic to peanuts", note},
		{"keywords:anuts", "Allergic to peanuts", ""},
		{"{content keywords}: anuts", note + "|Allergic to peanuts", note},
		// Syntax in a word that counts as matched through the trigram index.
		// Off, each memory holds one query word and bm25 ranks the shorter
		// first.
		{`allergic NEAR(anuts`, "Allergic to peanuts|" + note, "Allergic to peanuts|" + note},
		{`"anuts" AND allergic*`, "Allergic to peanuts|Likes ^start and -minus signs|" + note, "Allergic to peanuts|Likes ^start and -minus signs|" + note},
		{`content:anuts) allergic`, "Allergic to peanuts|" + note, "Allergic to peanuts|" + note},
		// Syntax between words that join: "pea" and "nuts" find "peanuts",
		// holding both, above the memories holding only "and".
		{`pea"nuts AND`, "Allergic to peanuts|Likes ^start and -minus signs|" + note, "Allergic to peanuts|Likes ^start and -minus signs|" + note},
		{`(pea)*"nuts" OR(`, "Allergic to peanuts|" + note, "Allergic to peanuts|" + note},
	} {
		tests = append(tests, struct{ query, want string }{tc.query, either(trigram, tc.on, tc.off)})
	}
	for _, tc := range tests {
		if got := recall(t, s, memory.RecallRequest{Query: tc.query}); got != tc.want {
			t.Errorf("recall(%q) = %q, want %q", tc.query, got, tc.want)
		}
	}
	checkIndex(t, s)
}

func TestKeywords(t *testing.T) { eachMode(t, testKeywords) }

func testKeywords(t *testing.T, trigram bool) {
	ctx := context.Background()
	s := newStore(t, WithTrigram(trigram))
	tagged := remember(t, s, "Booked a table for Friday", "restaurant", "Dinner plans")
	if strings.Join(tagged.Keywords, "|") != "restaurant|Dinner plans" {
		t.Errorf("remember keywords = %q", tagged.Keywords)
	}
	remember(t, s, "The restaurant list is long")
	// A keyword weighs more than the same word in content: the older,
	// keyword-tagged memory ranks first.
	if got := recall(t, s, memory.RecallRequest{Query: "restaurant"}); got != "Booked a table for Friday|The restaurant list is long" {
		t.Errorf("recall = %q", got)
	}
	if got := recall(t, s, memory.RecallRequest{Query: "dinner plans"}); got != "Booked a table for Friday" {
		t.Errorf("recall by keyword phrase = %q", got)
	}

	// Saving the same content again returns it as it is, keywords not merged.
	again, err := s.Remember(ctx, "Booked a table for Friday", []string{"other"})
	if err != nil || again.ID != tagged.ID || strings.Join(again.Keywords, "|") != "restaurant|Dinner plans" {
		t.Errorf("remember duplicate = %+v, %v", again, err)
	}
	if got := recall(t, s, memory.RecallRequest{Query: "other"}); got != "" {
		t.Errorf("duplicate's keywords were indexed: %q", got)
	}

	// Update with nil keeps keywords; with an empty slice clears them.
	u, err := s.Update(ctx, tagged.ID, "Booked a table for Saturday", nil)
	if err != nil || strings.Join(u.Keywords, "|") != "restaurant|Dinner plans" {
		t.Errorf("update nil keywords = %+v, %v", u, err)
	}
	if got := recall(t, s, memory.RecallRequest{Query: "dinner"}); got != "Booked a table for Saturday" {
		t.Errorf("kept keywords not indexed: %q", got)
	}
	if u, err = s.Update(ctx, tagged.ID, "Booked a table for Saturday", []string{}); err != nil || u.Keywords != nil {
		t.Errorf("update empty keywords = %+v, %v", u, err)
	}
	if got := recall(t, s, memory.RecallRequest{Query: "dinner"}); got != "" {
		t.Errorf("cleared keywords still indexed: %q", got)
	}
	if u, err = s.Update(ctx, tagged.ID, "Booked a table for Saturday", []string{" sushi "}); err != nil || strings.Join(u.Keywords, "|") != "sushi" {
		t.Errorf("update keywords = %+v, %v", u, err)
	}
	list, err := s.List(ctx, 0)
	if err != nil || len(list) != 2 || strings.Join(list[0].Keywords, "|") != "sushi" || list[1].Keywords != nil {
		t.Errorf("list = %+v, %v", list, err)
	}

	// Export carries each event's keywords.
	var got []string
	if err := s.Export(ctx, func(e memory.Event) error {
		got = append(got, strings.Join(e.Keywords, ","))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if g := strings.Join(got, "|"); g != "restaurant,Dinner plans||restaurant,Dinner plans||sushi" {
		t.Errorf("exported keywords = %q", g)
	}

	// Forget removes the memory from the index.
	if _, err := s.Forget(ctx, tagged.ID); err != nil {
		t.Fatal(err)
	}
	if got := recall(t, s, memory.RecallRequest{Query: "sushi saturday"}); got != "" {
		t.Errorf("forgotten memory recalled: %q", got)
	}
	checkIndex(t, s)

	// Invalid keywords write nothing.
	if _, err := s.Remember(ctx, "x", []string{""}); err != memory.ErrKeywordEmpty {
		t.Errorf("remember empty keyword = %v", err)
	}
	if _, err := s.Update(ctx, u.ID, "x", make([]string, memory.MaxKeywords+1)); err != memory.ErrKeywordsTooMany {
		t.Errorf("update too many keywords = %v", err)
	}
}

func TestRecallTimeWeight(t *testing.T) { eachMode(t, testRecallTimeWeight) }

func testRecallTimeWeight(t *testing.T, trigram bool) {
	ctx := context.Background()
	s := newStore(t, WithTrigram(trigram))

	// Equal relevance: the tie-break decides. Updating t1 makes it the newest.
	t1 := remember(t, s, "tea one")
	t2 := remember(t, s, "tea two")
	t3 := remember(t, s, "tea six")
	if _, err := s.Update(ctx, t1.ID, "tea won", nil); err != nil {
		t.Fatal(err)
	}
	byID := []memory.Memory{{ID: t1.ID, Content: "tea won"}, t2, t3}
	slices.SortFunc(byID, func(a, b memory.Memory) int { return strings.Compare(a.ID, b.ID) })
	for _, tc := range []struct {
		req  memory.RecallRequest
		want string
	}{
		{memory.RecallRequest{}, "tea won|tea six|tea two"},
		{memory.RecallRequest{Order: memory.OrderOldest}, "tea two|tea six|tea won"},
		{memory.RecallRequest{TimeWeight: memory.TimeWeightOff}, strings.Join(ids(byID), "|")},
		{memory.RecallRequest{TimeWeight: memory.TimeWeightOff, Order: memory.OrderOldest}, strings.Join(ids(byID), "|")},
	} {
		tc.req.Query = "tea"
		if got := recall(t, s, tc.req); got != tc.want {
			t.Errorf("recall(%+v) = %q, want %q", tc.req, got, tc.want)
		}
	}

	// Relevance A > B > C > D, saved B, A, C, D. High weight with newest:
	// A 0+2, B 1+3, C 2+1, D 3+0, so A, C, D, B.
	s = newStore(t, WithTrigram(trigram))
	remember(t, s, "Tea, then green")                   // B: every word
	remember(t, s, "Green tea")                         // A: the phrase
	remember(t, s, "Green")                             // C: one word, short
	remember(t, s, "Green grapes and some other fruit") // D: one word, long
	for _, tc := range []struct {
		req  memory.RecallRequest
		want string
	}{
		{memory.RecallRequest{}, "Green tea|Tea, then green|Green|Green grapes and some other fruit"},
		{memory.RecallRequest{TimeWeight: memory.TimeWeightHigh}, "Green tea|Green|Green grapes and some other fruit|Tea, then green"},
		{memory.RecallRequest{TimeWeight: memory.TimeWeightHigh, Order: memory.OrderOldest}, "Green tea|Tea, then green|Green|Green grapes and some other fruit"},
		{memory.RecallRequest{TimeWeight: memory.TimeWeightHigh, Limit: 2}, "Green tea|Green"},
	} {
		tc.req.Query = "green tea"
		if got := recall(t, s, tc.req); got != tc.want {
			t.Errorf("recall(%+v) = %q, want %q", tc.req, got, tc.want)
		}
	}

	for _, req := range []memory.RecallRequest{
		{Query: "tea", TimeWeight: "medium"},
		{Query: "tea", Order: "random"},
	} {
		if _, err := s.Recall(ctx, req); err == nil || !strings.Contains(err.Error(), "must be") {
			t.Errorf("recall(%+v) = %v, want a validation error", req, err)
		}
	}
}

// TestRecallRanking: more query words held beats a rarer single word; the
// best matches are those holding the most words, and related memories
// sharing more names come first.
func TestRecallRanking(t *testing.T) { eachMode(t, testRecallRanking) }

func testRecallRanking(t *testing.T, trigram bool) {
	s := newStore(t, WithTrigram(trigram))
	for _, c := range []string{
		"The acme deal is in negotiation with Dana Whitfield, who wants it all done by spring",
		"Contact Dana Whitfield by phone",
		"Negotiations",
		"Filler about acme one",
		"Filler about acme two",
		"Filler about acme three",
		"Weekly call where Whitfield joins",
	} {
		remember(t, s, c)
	}
	want := "The acme deal is in negotiation with Dana Whitfield, who wants it all done by spring|" +
		"Contact Dana Whitfield by phone|Weekly call where Whitfield joins|Negotiations"
	if got := recall(t, s, memory.RecallRequest{Query: "acme negotiation", Limit: 4}); got != want {
		t.Errorf("recall = %q, want %q", got, want)
	}
}

// TestRecallOffTiesByID: with time weight off, equal matches come in id
// order, whatever their age.
func TestRecallOffTiesByID(t *testing.T) { eachMode(t, testRecallOffTiesByID) }

func testRecallOffTiesByID(t *testing.T, trigram bool) {
	s := newStore(t, WithTrigram(trigram))
	// Saved in this order; ids order them six, won, two.
	for _, ev := range [][2]string{
		{"01JB8Z3K7Q9X2M4N6P8R0T2V4C", "tea won"},
		{"01JB8Z3K7Q9X2M4N6P8R0T2V4D", "tea two"},
		{"01JB8Z3K7Q9X2M4N6P8R0T2V4A", "tea six"},
	} {
		if _, err := s.db.Exec("INSERT INTO memory_events (memory_id, kind, content, created_at) VALUES (?, 'remember', ?, 1)", ev[0], ev[1]); err != nil {
			t.Fatal(err)
		}
	}
	for req, want := range map[memory.RecallRequest]string{
		{Query: "tea"}: "tea six|tea two|tea won",
		{Query: "tea", Order: memory.OrderOldest}:        "tea won|tea two|tea six",
		{Query: "tea", TimeWeight: memory.TimeWeightOff}: "tea six|tea won|tea two",
	} {
		if got := recall(t, s, req); got != want {
			t.Errorf("recall(%+v) = %q, want %q", req, got, want)
		}
	}
}

// TestRecallNoDuplicates: whatever the time weight, order and limit, recall
// never returns a memory twice. More best matches than the limit, all
// sharing a name, once sent best matches past the limit back through related.
func TestRecallNoDuplicates(t *testing.T) { eachMode(t, testRecallNoDuplicates) }

func testRecallNoDuplicates(t *testing.T, trigram bool) {
	base := []string{
		"Tea Zelda",
		"Tea with Zelda now",
		"Tea with Zelda and some words",
		"Tea with Zelda and a long tail of extra words here",
		"Tea with Zelda plus five more words",
		"Lunch with Zelda",
		"Tea time at Quill House with Zelda",
		"Quill House serves green tea",
		"teapot",
	}
	for _, rev := range []bool{false, true} {
		saved := slices.Clone(base)
		if rev {
			slices.Reverse(saved)
		}
		s := newStore(t, WithTrigram(trigram))
		for _, c := range saved {
			remember(t, s, c)
		}
		for i := range 20 {
			remember(t, s, fmt.Sprintf("filler number %d", i))
		}
		for _, q := range []string{"tea", "zelda", "tea zelda", "quill tea", "eapo"} {
			for _, tw := range []string{memory.TimeWeightOff, memory.TimeWeightLow, memory.TimeWeightHigh} {
				for _, o := range []string{memory.OrderNewest, memory.OrderOldest} {
					for limit := 1; limit <= len(base)+1; limit++ {
						req := memory.RecallRequest{Query: q, Limit: limit, TimeWeight: tw, Order: o}
						got, err := s.Recall(context.Background(), req)
						if err != nil {
							t.Fatalf("recall(%+v): %v", req, err)
						}
						seen := map[string]bool{}
						for _, m := range got {
							if seen[m.ID] {
								t.Errorf("reversed=%v recall(%+v) returns %q twice: %q", rev, req, m.Content, ids(got))
							}
							seen[m.ID] = true
						}
						if len(got) > limit {
							t.Errorf("reversed=%v recall(%+v) returns %d > limit", rev, req, len(got))
						}
					}
				}
			}
		}
	}
}

func TestByRecency(t *testing.T) {
	t.Parallel()
	hits := func() []hit {
		var out []hit
		for i, seq := range []int64{1, 4, 2, 3} {
			out = append(out, hit{memory.Memory{ID: string(rune('a' + i))}, seq})
		}
		return out
	}
	for order, want := range map[string]string{
		// newest: recency ranks b0 d1 c2 a3; keys a3 b1 c4 d4.
		memory.OrderNewest: "b|a|c|d",
		// oldest: recency ranks a0 c1 d2 b3; keys a0 b4 c3 d5.
		memory.OrderOldest: "a|c|b|d",
	} {
		h := hits()
		byRecency(h, order)
		var got []string
		for _, x := range h {
			got = append(got, x.m.ID)
		}
		if g := strings.Join(got, "|"); g != want {
			t.Errorf("%s: %s, want %s", order, g, want)
		}
	}
}

// TestMigrateFromV1: a version 1 database keeps its history and gains an
// index built from it.
func TestMigrateFromV1(t *testing.T) { eachMode(t, testMigrateFromV1) }

func testMigrateFromV1(t *testing.T, trigram bool) {
	path := t.TempDir() + "/wenna.db"
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	const a, b = "01JB8Z3K7Q9X2M4N6P8R0T2V4A", "01JB8Z3K7Q9X2M4N6P8R0T2V4B"
	for _, ev := range [][3]any{
		{a, "remember", "Our first customer is Acme Robotics"},
		{b, "remember", "Old text about Acme"},
		{b, "remember", "Acme Robotics renews on March 1"},
		{a, "forget", nil},
	} {
		if _, err := db.Exec("INSERT INTO memory_events (memory_id, kind, content, created_at) VALUES (?, ?, ?, 1)", ev[0], ev[1], ev[2]); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s := open(t, path, WithTrigram(trigram))
	var v int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != schemaVersion {
		t.Fatalf("user_version = %d, %v", v, err)
	}
	checkIndex(t, s)
	if got := recall(t, s, memory.RecallRequest{Query: "acme"}); got != "Acme Robotics renews on March 1" {
		t.Errorf("recall after migration = %q", got)
	}
	var n int
	if err := s.db.QueryRow("SELECT count(*) FROM memory_events").Scan(&n); err != nil || n != 4 {
		t.Errorf("events = %d, %v; want the 4 kept", n, err)
	}
}

// TestIndexCatchesUpWithOlderWriter: an older wenna still running writes
// events without touching the index; recall indexes them before reading.
func TestIndexCatchesUpWithOlderWriter(t *testing.T) { eachMode(t, testIndexCatchesUpWithOlderWriter) }

func testIndexCatchesUpWithOlderWriter(t *testing.T, trigram bool) {
	s := newStore(t, WithTrigram(trigram))
	m := remember(t, s, "Written by this wenna")
	for _, q := range []string{
		"INSERT INTO memory_events (memory_id, kind, content, created_at) VALUES ('01JB8Z3K7Q9X2M4N6P8R0T2V4W', 'remember', 'Written by an older wenna', 1)",
		"INSERT INTO memory_events (memory_id, kind, content, created_at) VALUES ('" + m.ID + "', 'forget', NULL, 1)",
	} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if got := recall(t, s, memory.RecallRequest{Query: "written wenna"}); got != "Written by an older wenna" {
		t.Errorf("recall = %q, want the older wenna's memory and not the forgotten one", got)
	}
	checkIndex(t, s)
}

// TestRecallTotal: the total counts the memories matching as well as the
// best one, however few the limit returns; not related memories or weaker
// matches. Past the candidate cap the index counts them: by the phrase, or by
// every query word.
func TestRecallTotal(t *testing.T) { eachMode(t, testRecallTotal) }

func testRecallTotal(t *testing.T, trigram bool) {
	ctx := context.Background()
	s := newStore(t, WithTrigram(trigram))
	remember(t, s, "Our first customer is Acme Robotics")
	remember(t, s, "Dana Whitfield is our contact at Acme Robotics")
	remember(t, s, "The customer support tool is Intercom")
	remember(t, s, "Demo day is on Friday")
	check := func(query string, limit, wantN, wantTotal int) {
		t.Helper()
		got, total, err := s.RecallTotal(ctx, memory.RecallRequest{Query: query, Limit: limit})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != wantN || total != wantTotal {
			t.Errorf("recall(%q, limit %d) = %d memories, total %d; want %d, total %d", query, limit, len(got), total, wantN, wantTotal)
		}
	}
	// The phrase match; the related contact and the weaker "customer" match
	// are returned but not counted.
	check("first customer", 0, 3, 1)
	check("nothing matches", 0, 0, 0)
	for _, who := range []string{"Sam", "Wei", "Omar"} {
		remember(t, s, "Met "+who+" at SaaStr")
	}
	check("SaaStr", 1, 1, 3) // a total past the limit
	for i := range candidates + 50 {
		remember(t, s, fmt.Sprintf("Promised client %d a demo", i))
	}
	check("promised", 5, 5, candidates+50)                                // by the phrase
	check("client demo", memory.MaxLimit, memory.MaxLimit, candidates+50) // by every word, not "Demo day"
	check("Promised client 7", 0, memory.DefaultLimit, 1)                 // one has the phrase; the rest are weaker
	remember(t, s, "A big demo for the board")                            // both words of "a demo", not the phrase
	check("a demo", 5, 5, candidates+50)                                  // by the phrase
}
