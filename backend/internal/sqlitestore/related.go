package sqlitestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
)

// minCommonName is the fewest memories a name must be in before it can count
// as common; above that, a name in more than 1/commonNameShare of all
// memories is common. A common name is the user's own world ("Lumen Labs"),
// and following it would return everything.
const (
	minCommonName   = 3
	commonNameShare = 4
)

// How a word is written in one memory: memory_words.flags.
const (
	wordLower   = 1 // starts lower-case
	wordUpper   = 2 // starts upper-case, two or more letters
	wordMidCaps = 4 // wordUpper, other than as the memory's first word
)

func splitWords(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
}

// wordFlags returns each case-folded word of content with how it is written
// there. Words written neither way (digits, uncased scripts) are left out.
func wordFlags(fold cases.Caser, content string) map[string]int {
	out := map[string]int{}
	for i, w := range splitWords(content) {
		r, _ := utf8.DecodeRuneInString(w)
		switch {
		case unicode.IsLower(r):
			out[fold.String(w)] |= wordLower
		case unicode.IsUpper(r) && utf8.RuneCountInString(w) > 1:
			f := wordUpper
			if i > 0 {
				f |= wordMidCaps
			}
			out[fold.String(w)] |= f
		}
	}
	return out
}

// relatedSQL finds the memories outside ?1 and ?4 (JSON arrays of
// memories.id) that share a name with a memory in ?1, most names shared
// first. A name is a
// word written upper-case in its memory, that some memory capitalizes other
// than as its first word, and that no memory writes in lower case: "Acme",
// not "The" or a leading "Prefers". Names in more than ?2 memories are common
// and not followed. Flags 1, 2, 4 are wordLower, wordUpper, wordMidCaps.
// TIEBREAK is replaced with the tie-break ORDER BY terms.
const relatedSQL = `
	WITH direct(id) AS (SELECT value FROM json_each(?1)),
	leads(word) AS (
		SELECT DISTINCT w.word FROM memory_words w
		WHERE w.id IN (SELECT id FROM direct) AND w.flags & 2
		AND NOT EXISTS (SELECT 1 FROM memory_words l WHERE l.word = w.word AND l.flags & 1)
		AND EXISTS (SELECT 1 FROM memory_words c WHERE c.word = w.word AND c.flags & 4)
		AND (SELECT count(*) FROM memory_words n WHERE n.word = w.word AND n.flags & 2) <= ?2
	)
	SELECT m.memory_id, e.content, e.keywords, e.created_at, m.seq
	FROM memory_words w
	JOIN leads USING (word)
	JOIN memories m ON m.id = w.id
	JOIN memory_events e ON e.seq = m.seq
	WHERE w.flags & 2 AND w.id NOT IN (SELECT id FROM direct)
	AND w.id NOT IN (SELECT value FROM json_each(?4))
	GROUP BY w.id
	ORDER BY count(*) DESC, TIEBREAK
	LIMIT ?3`

// related returns up to limit memories sharing a name with the memories
// whose memories.id is in direct: the second lookup a model often skips.
// "first customer" finds "Our first customer is Acme Robotics"; related finds
// "Our contact at Acme Robotics is Dana". Memories in exclude, already
// ranked, are not returned. tieBreak is one of the fixed tieBreaks, never
// input.
func related(ctx context.Context, db *sql.DB, direct, exclude []int64, tieBreak string, limit int) (out []hit, err error) {
	if len(direct) == 0 {
		return nil, nil
	}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM memories").Scan(&n); err != nil {
		return nil, err
	}
	ids, err := json.Marshal(direct)
	if err != nil {
		return nil, err
	}
	if exclude == nil {
		exclude = []int64{} // json.Marshal writes nil as null
	}
	ex, err := json.Marshal(exclude)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, strings.Replace(relatedSQL, "TIEBREAK", tieBreak, 1),
		string(ids), max(minCommonName, n/commonNameShare), limit, string(ex))
	if err != nil {
		return nil, err
	}
	defer closeRows(rows, &err)
	for rows.Next() {
		var h hit
		if h.m, err = scanMemory(rows, &h.seq); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
