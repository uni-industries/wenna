// Package memory holds the memory domain: what a memory is and which inputs
// are valid. It is the one validator for memory input (docs/controls.md);
// stores and transports call it, never restate it.
package memory

import (
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/uni-industries/wenna/backend/internal/id"
)

// Limits on input. Properties of the product, not of an environment.
const (
	MaxContentRunes = 4000
	MaxQueryRunes   = 500
	DefaultLimit    = 20
	MaxLimit        = 100
	MaxKeywords     = 10
	MaxKeywordRunes = 50
)

// Memory is the current value of one memory, derived from its history.
// Keywords are the AI's extra search terms for it, part of its value.
type Memory struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	Keywords  []string  `json:"keywords,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Event is one entry of a memory's history. Content and Keywords are nil for
// a forget; a remember carries the keywords current after it.
type Event struct {
	Seq      int64     `json:"seq"`
	MemoryID string    `json:"memory_id"`
	Kind     string    `json:"kind"`
	Content  *string   `json:"content,omitempty"`
	Keywords []string  `json:"keywords,omitempty"`
	At       time.Time `json:"at"`
}

// TimeWeight values: how much recency counts in recall.
const (
	TimeWeightHigh = "high" // relevance and recency count equally
	TimeWeightLow  = "low"  // relevance; recency breaks ties
	TimeWeightOff  = "off"  // relevance; ties by id
)

// Order values: which end of time recency favours.
const (
	OrderNewest = "newest"
	OrderOldest = "oldest"
)

// RecallRequest is one recall. Zero Limit, TimeWeight and Order take their
// defaults (DefaultLimit, TimeWeightLow, OrderNewest) in Validate.
type RecallRequest struct {
	Query      string
	Limit      int
	TimeWeight string
	Order      string
}

// ErrInvalid marks input a caller can fix: every validation error matches it
// with errors.Is. Its own message is a fixed sentence, safe to show.
var ErrInvalid = errors.New("The input is not valid.")

// InvalidError is one validation failure. Its message is a fixed sentence,
// safe to show as is; it never carries the input.
type InvalidError struct{ msg string }

func (e *InvalidError) Error() string { return e.msg }

// Is makes every InvalidError match ErrInvalid.
func (e *InvalidError) Is(target error) bool { return target == ErrInvalid }

// The validation failures. Compare by identity.
var (
	ErrContentEmpty    = &InvalidError{"Content is empty."}
	ErrContentTooLong  = &InvalidError{"Content is too long: " + strconv.Itoa(MaxContentRunes) + " characters at most."}
	ErrContentNotText  = &InvalidError{"Content must be plain text."}
	ErrQueryEmpty      = &InvalidError{"Query is empty."}
	ErrQueryTooLong    = &InvalidError{"Query is too long: " + strconv.Itoa(MaxQueryRunes) + " characters at most."}
	ErrQueryNotText    = &InvalidError{"Query must be plain text."}
	ErrLimit           = &InvalidError{"Limit must be between 1 and " + strconv.Itoa(MaxLimit) + "."}
	ErrID              = &InvalidError{"That is not a memory id."}
	ErrKeywordsTooMany = &InvalidError{"At most " + strconv.Itoa(MaxKeywords) + " keywords."}
	ErrKeywordEmpty    = &InvalidError{"A keyword is empty."}
	ErrKeywordTooLong  = &InvalidError{"A keyword is too long: " + strconv.Itoa(MaxKeywordRunes) + " characters at most."}
	ErrKeywordNotText  = &InvalidError{"Keywords must be plain text."}
	ErrTimeWeight      = &InvalidError{"Time weight must be high, low or off."}
	ErrOrder           = &InvalidError{"Order must be newest or oldest."}
)

// ErrNotFound means no current memory with that id exists: it never
// existed, or was forgotten.
var ErrNotFound = errors.New("memory not found")

type textRule struct {
	maxRunes                int
	empty, tooLong, notText *InvalidError
}

var (
	contentRule = textRule{MaxContentRunes, ErrContentEmpty, ErrContentTooLong, ErrContentNotText}
	queryRule   = textRule{MaxQueryRunes, ErrQueryEmpty, ErrQueryTooLong, ErrQueryNotText}
	keywordRule = textRule{MaxKeywordRunes, ErrKeywordEmpty, ErrKeywordTooLong, ErrKeywordNotText}
)

// Content validates and normalises memory text.
func Content(s string) (string, error) { return text(s, contentRule) }

// Query validates and normalises a recall query.
func Query(s string) (string, error) { return text(s, queryRule) }

// Keywords validates and normalises a memory's keywords into a new slice. Nil
// stays nil (an update keeps the old keywords); an empty non-nil slice stays
// empty (an update clears them).
func Keywords(ks []string) ([]string, error) {
	if ks == nil {
		return nil, nil
	}
	if len(ks) > MaxKeywords {
		return nil, ErrKeywordsTooMany
	}
	out := make([]string, 0, len(ks))
	for _, k := range ks {
		k, err := text(k, keywordRule)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, nil
}

// Validate checks r and returns it normalised, with defaults filled in. An
// unknown TimeWeight or Order is an error, never a default.
func (r RecallRequest) Validate() (RecallRequest, error) {
	var err error
	if r.Query, err = Query(r.Query); err != nil {
		return RecallRequest{}, err
	}
	if r.Limit, err = Limit(r.Limit); err != nil {
		return RecallRequest{}, err
	}
	switch r.TimeWeight {
	case "":
		r.TimeWeight = TimeWeightLow
	case TimeWeightHigh, TimeWeightLow, TimeWeightOff:
	default:
		return RecallRequest{}, ErrTimeWeight
	}
	switch r.Order {
	case "":
		r.Order = OrderNewest
	case OrderNewest, OrderOldest:
	default:
		return RecallRequest{}, ErrOrder
	}
	return r, nil
}

func text(s string, r textRule) (string, error) {
	// NUL truncates text in many tools that read the file; refuse it.
	if !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
		return "", r.notText
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", r.empty
	}
	if utf8.RuneCountInString(s) > r.maxRunes {
		return "", r.tooLong
	}
	return s, nil
}

// Limit validates a result count; 0 means DefaultLimit.
func Limit(n int) (int, error) {
	switch {
	case n == 0:
		return DefaultLimit, nil
	case n < 0 || n > MaxLimit:
		return 0, ErrLimit
	}
	return n, nil
}

// ID validates a memory id and returns its canonical form.
func ID(s string) (string, error) {
	canon, err := id.Parse(s)
	if err != nil {
		return "", ErrID
	}
	return canon, nil
}
