// Package id issues and parses wenna's identifiers: ULIDs in their canonical
// 26-character upper-case form. It is the one implementation
// (docs/controls.md); nothing else generates or parses an id.
package id

import (
	"crypto/rand"
	"errors"
	"strings"

	"github.com/oklog/ulid/v2"
)

// ErrInvalid means the text is not a ULID.
var ErrInvalid = errors.New("not a ULID")

// New returns a fresh ULID. Entropy comes from crypto/rand, not the library's
// default clock-seeded source: instances starting in the same nanosecond must
// not share a random stream.
func New() string {
	// crypto/rand.Reader never returns an error, so MustNew cannot panic.
	return ulid.MustNew(ulid.Now(), rand.Reader).String()
}

// Parse validates s as a ULID and returns its canonical form. Surrounding
// space is trimmed and lower case accepted, since people and models retype
// ids.
func Parse(s string) (string, error) {
	u, err := ulid.ParseStrict(strings.ToUpper(strings.TrimSpace(s)))
	if err != nil {
		return "", ErrInvalid
	}
	return u.String(), nil
}
