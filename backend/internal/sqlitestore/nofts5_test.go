//go:build !sqlite_fts5

package sqlitestore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// TestOpenWithoutFTS5 runs only in a build without the sqlite_fts5 tag, the
// one place the refusal can be seen: Open must fail plainly, never fall back.
// Every other test in this package needs the tag and fails without it.
func TestOpenWithoutFTS5(t *testing.T) {
	t.Parallel()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "wenna.db"))
	if !errors.Is(err, ErrNoFTS5) {
		if err == nil {
			err = s.Close()
		}
		t.Fatalf("Open = %v, want ErrNoFTS5", err)
	}
}
