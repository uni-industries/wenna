package memory

import (
	"errors"
	"strings"
	"testing"
)

func TestContentAndQuery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fn      func(string) (string, error)
		in      string
		want    string
		wantErr error
	}{
		{name: "content plain", fn: Content, in: "likes tea", want: "likes tea"},
		{name: "content trimmed", fn: Content, in: "  likes tea\n", want: "likes tea"},
		{name: "content unicode", fn: Content, in: "café ☕ 日本", want: "café ☕ 日本"},
		{name: "content at limit", fn: Content, in: strings.Repeat("é", MaxContentRunes), want: strings.Repeat("é", MaxContentRunes)},
		{name: "content over limit", fn: Content, in: strings.Repeat("a", MaxContentRunes+1), wantErr: ErrContentTooLong},
		{name: "content far over limit", fn: Content, in: strings.Repeat("a", 3*MaxContentRunes), wantErr: ErrContentTooLong},
		{name: "content empty", fn: Content, in: "", wantErr: ErrContentEmpty},
		{name: "content whitespace", fn: Content, in: " \t\n", wantErr: ErrContentEmpty},
		{name: "content NUL", fn: Content, in: "a\x00b", wantErr: ErrContentNotText},
		{name: "content invalid utf8", fn: Content, in: "a\xffb", wantErr: ErrContentNotText},
		{name: "content sql-shaped kept verbatim", fn: Content, in: "'); DROP TABLE memory_events;--", want: "'); DROP TABLE memory_events;--"},
		{name: "query plain", fn: Query, in: "tea", want: "tea"},
		{name: "query at limit", fn: Query, in: strings.Repeat("q", MaxQueryRunes), want: strings.Repeat("q", MaxQueryRunes)},
		{name: "query over limit", fn: Query, in: strings.Repeat("q", MaxQueryRunes+1), wantErr: ErrQueryTooLong},
		{name: "query empty", fn: Query, in: "", wantErr: ErrQueryEmpty},
		{name: "query NUL", fn: Query, in: "\x00", wantErr: ErrQueryNotText},
		{name: "query invalid utf8", fn: Query, in: "\xff", wantErr: ErrQueryNotText},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tc.fn(tc.in)
			if tc.wantErr != nil {
				// Identity, not just errors.Is: the message must be the
				// constant itself, with nothing wrapped around it.
				if err != tc.wantErr || !errors.Is(err, ErrInvalid) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("= %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestLimit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      int
		want    int
		wantErr error
	}{
		{in: 0, want: DefaultLimit},
		{in: 1, want: 1},
		{in: MaxLimit, want: MaxLimit},
		{in: MaxLimit + 1, wantErr: ErrLimit},
		{in: -1, wantErr: ErrLimit},
	}
	for _, tc := range tests {
		got, err := Limit(tc.in)
		if err != tc.wantErr || got != tc.want {
			t.Errorf("Limit(%d) = %d, %v; want %d, %v", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestKeywords(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("é", MaxKeywordRunes)
	tests := []struct {
		name    string
		in      []string
		want    []string // nil and empty are distinct
		wantErr error
	}{
		{name: "nil keeps", in: nil, want: nil},
		{name: "empty clears", in: []string{}, want: []string{}},
		{name: "trimmed", in: []string{" Acme ", "billing"}, want: []string{"Acme", "billing"}},
		{name: "at rune limit", in: []string{long}, want: []string{long}},
		{name: "at count limit", in: make10("k"), want: make10("k")},
		{name: "over count limit", in: append(make10("k"), "k"), wantErr: ErrKeywordsTooMany},
		{name: "over rune limit", in: []string{long + "a"}, wantErr: ErrKeywordTooLong},
		{name: "empty keyword", in: []string{"a", "  "}, wantErr: ErrKeywordEmpty},
		{name: "NUL", in: []string{"a\x00"}, wantErr: ErrKeywordNotText},
		{name: "invalid utf8", in: []string{"\xff"}, wantErr: ErrKeywordNotText},
		{name: "fts syntax kept verbatim", in: []string{`"a" NEAR(b) *`}, want: []string{`"a" NEAR(b) *`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := Keywords(tc.in)
			if tc.wantErr != nil {
				if err != tc.wantErr || !errors.Is(err, ErrInvalid) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil || (got == nil) != (tc.want == nil) || strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("= %#v, %v; want %#v", got, err, tc.want)
			}
		})
	}
	// The caller's slice is not rewritten.
	in := []string{" a "}
	if _, err := Keywords(in); err != nil || in[0] != " a " {
		t.Errorf("input = %q, %v; want it untouched", in, err)
	}
}

func make10(s string) []string {
	out := make([]string, MaxKeywords)
	for i := range out {
		out[i] = s
	}
	return out
}

func TestRecallRequest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      RecallRequest
		want    RecallRequest
		wantErr error
	}{
		{name: "defaults", in: RecallRequest{Query: " tea "}, want: RecallRequest{Query: "tea", Limit: DefaultLimit, TimeWeight: TimeWeightLow, Order: OrderNewest}},
		{name: "all set", in: RecallRequest{Query: "tea", Limit: 3, TimeWeight: TimeWeightHigh, Order: OrderOldest}, want: RecallRequest{Query: "tea", Limit: 3, TimeWeight: TimeWeightHigh, Order: OrderOldest}},
		{name: "off", in: RecallRequest{Query: "tea", TimeWeight: TimeWeightOff}, want: RecallRequest{Query: "tea", Limit: DefaultLimit, TimeWeight: TimeWeightOff, Order: OrderNewest}},
		{name: "empty query", in: RecallRequest{Query: " "}, wantErr: ErrQueryEmpty},
		{name: "bad limit", in: RecallRequest{Query: "tea", Limit: -1}, wantErr: ErrLimit},
		{name: "unknown time weight", in: RecallRequest{Query: "tea", TimeWeight: "medium"}, wantErr: ErrTimeWeight},
		{name: "time weight is case-sensitive", in: RecallRequest{Query: "tea", TimeWeight: "HIGH"}, wantErr: ErrTimeWeight},
		{name: "unknown order", in: RecallRequest{Query: "tea", Order: "random"}, wantErr: ErrOrder},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tc.in.Validate()
			if tc.wantErr != nil {
				if err != tc.wantErr || !errors.Is(err, ErrInvalid) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("= %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

// TestID: memory ids go through package id; this pins that a malformed id
// is ErrID. id's own tests cover the format.
func TestID(t *testing.T) {
	t.Parallel()
	const good = "01JB8Z3K7Q9X2M4N6P8R0T2V4W"
	if got, err := ID(" 01jb8z3k7q9x2m4n6p8r0t2v4w "); err != nil || got != good {
		t.Errorf("ID(lower, padded) = %q, %v; want %q", got, err, good)
	}
	for _, in := range []string{"", "0d9f2c1e-8b7a-4c3d-9e2f-1a2b3c4d5e6f", "1 OR 1=1"} {
		if _, err := ID(in); err != ErrID {
			t.Errorf("ID(%q) error = %v, want ErrID", in, err)
		}
	}
}

// TestMessagesAreFixedSentences pins the exact text every validation failure
// shows: a plain sentence with no prefix and nothing from the input.
func TestMessagesAreFixedSentences(t *testing.T) {
	t.Parallel()
	want := map[*InvalidError]string{
		ErrContentEmpty:    "Content is empty.",
		ErrContentTooLong:  "Content is too long: 4000 characters at most.",
		ErrContentNotText:  "Content must be plain text.",
		ErrQueryEmpty:      "Query is empty.",
		ErrQueryTooLong:    "Query is too long: 500 characters at most.",
		ErrQueryNotText:    "Query must be plain text.",
		ErrLimit:           "Limit must be between 1 and 100.",
		ErrID:              "That is not a memory id.",
		ErrKeywordsTooMany: "At most 10 keywords.",
		ErrKeywordEmpty:    "A keyword is empty.",
		ErrKeywordTooLong:  "A keyword is too long: 50 characters at most.",
		ErrKeywordNotText:  "Keywords must be plain text.",
		ErrTimeWeight:      "Time weight must be high, low or off.",
		ErrOrder:           "Order must be newest or oldest.",
	}
	for e, msg := range want {
		if e.Error() != msg {
			t.Errorf("message = %q, want %q", e.Error(), msg)
		}
		if !errors.Is(e, ErrInvalid) {
			t.Errorf("%q does not match ErrInvalid", msg)
		}
	}
	if errors.Is(ErrNotFound, ErrInvalid) {
		t.Error("ErrNotFound matches ErrInvalid")
	}
}
