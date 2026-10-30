package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

const canary = "canary-pw-7f3a"

// undefended mirrors Serve's shape with a plain string: the vacuity control.
// Every renderer must leak it, or "absent" proves nothing.
type undefended struct {
	Addr  string
	Token string
}

// renderings returns v through every verb, JSON, and both slog handlers.
func renderings(t *testing.T, v any) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		out[verb] = fmt.Sprintf(verb, v)
	}
	js, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	out["json"] = string(js)
	var text, jsonLog bytes.Buffer
	slog.New(slog.NewTextHandler(&text, nil)).Info("m", "v", v)
	slog.New(slog.NewJSONHandler(&jsonLog, nil)).Info("m", "v", v)
	out["slog text"] = text.String()
	out["slog json"] = jsonLog.String()
	return out
}

func TestSecretNeverRenders(t *testing.T) {
	t.Parallel()

	cases := map[string]any{
		"secret":         Secret{v: canary},
		"secret pointer": &Secret{v: canary},
		"serve":          Serve{Addr: ":1", Token: Secret{v: canary}},
		"serve pointer":  &Serve{Addr: ":1", Token: Secret{v: canary}},
	}
	for name, v := range cases {
		for how, s := range renderings(t, v) {
			if strings.Contains(s, canary) {
				t.Errorf("%s via %s leaks: %s", name, how, s)
			}
		}
	}

	for how, s := range renderings(t, undefended{Addr: ":1", Token: canary}) {
		if !strings.Contains(s, canary) {
			t.Errorf("vacuity control via %s did not leak, so the absence checks prove nothing: %s", how, s)
		}
	}
}

// TestServeLogValueProviders: the log line says which providers are on and
// their non-secret identifiers, and never an owner's email or ID.
func TestServeLogValueProviders(t *testing.T) {
	t.Parallel()
	const email, subject = "owner-canary@example.com", "user_canary01"
	tests := []struct {
		name    string
		serve   Serve
		want    []string
		mustNot []string
	}{
		{
			name:  "off",
			serve: Serve{Addr: ":1"},
			want:  []string{"v.access.on=false", "v.workos.on=false", `v.public_url=""`},
		},
		{
			name:  "public url",
			serve: Serve{Addr: ":1", PublicURL: "https://m.example.ts.net"},
			want:  []string{"v.public_url=https://m.example.ts.net"},
		},
		{
			name: "on",
			serve: Serve{
				Addr:   ":1",
				Access: &Access{TeamDomain: "acme.example.com", Audience: "aud-tag", OwnerEmail: email},
				WorkOS: &WorkOS{Issuer: "https://auth.example.org", Resource: "https://m.example.com/api/mcp", OwnerSubject: subject},
			},
			want: []string{
				"v.access.on=true", "v.access.team_domain=acme.example.com", "v.access.aud=aud-tag", "v.access.owner_email_set=true",
				"v.workos.on=true", "v.workos.issuer=https://auth.example.org", "v.workos.resource=https://m.example.com/api/mcp", "v.workos.owner_id_set=true",
			},
			mustNot: []string{email, subject},
		},
		{
			name:  "access only",
			serve: Serve{Addr: ":1", Access: &Access{TeamDomain: "acme.example.com", Audience: "aud-tag", OwnerEmail: email}},
			want:  []string{"v.access.on=true", "v.workos.on=false"},
		},
		{
			name:  "workos only",
			serve: Serve{Addr: ":1", WorkOS: &WorkOS{Issuer: "https://auth.example.org", Resource: "https://m.example.com/api/mcp", OwnerSubject: subject}},
			want:  []string{"v.access.on=false", "v.workos.on=true"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			slog.New(slog.NewTextHandler(&buf, nil)).Info("m", "v", tc.serve)
			got := buf.String()
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("log %q lacks %q", got, w)
				}
			}
			for _, n := range tc.mustNot {
				if strings.Contains(got, n) {
					t.Errorf("log %q leaks %q", got, n)
				}
			}
		})
	}
}

func TestRevealReturnsValue(t *testing.T) {
	t.Parallel()
	if got := (Secret{v: canary}).Reveal(); got != canary {
		t.Fatalf("Reveal() = %q, want %q", got, canary)
	}
}
