// Package bearer reads "Authorization: Bearer <token>" and compares it to the
// configured token in constant time. Package gate decides admission; this
// package only answers whether a request carries the token.
package bearer

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// Matches returns a check that reports whether a request's bearer token is
// token. An empty token matches nothing: FromHeader never reports an empty
// token as present.
func Matches(token string) func(*http.Request) bool {
	// Compared as digests: equal length, so the comparison's time says
	// nothing about the token's length or content.
	want := sha256.Sum256([]byte(token))
	return func(r *http.Request) bool {
		got, ok := FromHeader(r.Header.Get("Authorization"))
		gotSum := sha256.Sum256([]byte(got))
		eq := subtle.ConstantTimeCompare(gotSum[:], want[:]) == 1
		return ok && eq
	}
}

// FromHeader returns the token of a "Bearer" Authorization header value. The
// scheme is case-insensitive (RFC 9110 § 11.1); an empty token is no token.
func FromHeader(h string) (string, bool) {
	scheme, tok, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	tok = strings.TrimSpace(tok)
	return tok, tok != ""
}
