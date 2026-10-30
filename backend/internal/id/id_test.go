package id

import (
	"errors"
	"testing"
)

func TestNew(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	prev := ""
	for range 1000 {
		got := New()
		if len(got) != 26 {
			t.Fatalf("New() = %q, want 26 characters", got)
		}
		if canon, err := Parse(got); err != nil || canon != got {
			t.Fatalf("Parse(New()) = %q, %v; want %q", canon, err, got)
		}
		if seen[got] {
			t.Fatalf("New() repeated %q", got)
		}
		seen[got] = true
		// The leading 10 characters are the millisecond timestamp.
		if got[:10] < prev {
			t.Fatalf("New() timestamp went backwards: %q after %q", got[:10], prev)
		}
		prev = got[:10]
	}
}

func TestParse(t *testing.T) {
	t.Parallel()
	const good = "01JB8Z3K7Q9X2M4N6P8R0T2V4W"
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: good, want: good},
		{in: " 01jb8z3k7q9x2m4n6p8r0t2v4w\n", want: good},
		{in: "", wantErr: true},
		{in: good[:25], wantErr: true},
		{in: good + "0", wantErr: true},
		{in: "01JB8Z3K7Q9X2M4N6P8R0T2V4U", wantErr: true}, // U is not Crockford base32
		{in: "01JB8Z3K7Q9X2M4N6P8R0T2V4!", wantErr: true},
		{in: "80000000000000000000000000", wantErr: true},           // timestamp overflows 48 bits
		{in: "0d9f2c1e-8b7a-4c3d-9e2f-1a2b3c4d5e6f", wantErr: true}, // a UUID
		{in: "' OR '1'='1' --------------", wantErr: true},
		{in: "01JB8Z3K7Q9X2M4N6P8R0T2V4é", wantErr: true},
	}
	for _, tc := range tests {
		got, err := Parse(tc.in)
		if tc.wantErr != errors.Is(err, ErrInvalid) || got != tc.want {
			t.Errorf("Parse(%q) = %q, %v; want %q, err %v", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}
