package bearer

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const token = "a-token-of-at-least-thirty-two-bytes-0123"

func TestMatches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, token, header string
		want                bool
	}{
		{"valid", token, "Bearer " + token, true},
		{"scheme case-insensitive", token, "bearer " + token, true},
		{"surrounding space", token, "Bearer  " + token + " ", true},
		{"missing", token, "", false},
		{"wrong token", token, "Bearer " + token + "x", false},
		{"prefix of token", token, "Bearer " + token[:10], false},
		{"empty token", token, "Bearer ", false},
		{"no scheme", token, token, false},
		{"basic scheme", token, "Basic " + token, false},
		{"scheme glued to token", token, "Bearer" + token, false},
		// An unset token must never be satisfied by an empty or absent one.
		{"unset token, no header", "", "", false},
		{"unset token, empty bearer", "", "Bearer ", false},
		{"unset token, any bearer", "", "Bearer x", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			if got := Matches(tc.token)(req); got != tc.want {
				t.Errorf("Matches = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFromHeader(t *testing.T) {
	t.Parallel()
	tests := []struct {
		header, want string
		ok           bool
	}{
		{"Bearer abc", "abc", true},
		{"BEARER abc", "abc", true},
		{"Bearer a.b.c", "a.b.c", true},
		{"Bearer ", "", false},
		{"Bearer    ", "", false},
		{"Basic abc", "", false},
		{"abc", "", false},
		{"", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.header, func(t *testing.T) {
			t.Parallel()
			got, ok := FromHeader(tc.header)
			if got != tc.want || ok != tc.ok {
				t.Errorf("FromHeader(%q) = %q, %v; want %q, %v", tc.header, got, ok, tc.want, tc.ok)
			}
		})
	}
}
