package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/uni-industries/wenna/backend/internal/config"
	"github.com/uni-industries/wenna/backend/internal/gate"
)

// providerHost is the host the fake providers answer as: httptest's TLS
// certificate names it, and config accepts it as a team domain.
const providerHost = "example.com"

// TestProtectionTurnsOnVerifiers: configuring a provider is what makes
// protection() admit that provider's owner token. Real tokens, signed with an
// in-test key and served as a JWKS over TLS.
func TestProtectionTurnsOnVerifiers(t *testing.T) {
	t.Parallel()
	const (
		issuer   = "https://" + providerHost
		resource = "https://memory.example.com/api/mcp"
		audTag   = "aud-tag"
		owner    = "owner@example.com"
		ownerSub = "user_01OWNER"
	)
	accessEnv := map[string]string{
		config.EnvAccessTeamDomain: providerHost,
		config.EnvAccessAudience:   audTag,
		config.EnvAccessOwnerEmail: owner,
	}
	workosEnv := map[string]string{
		config.EnvWorkOSIssuer:   issuer,
		config.EnvWorkOSResource: resource,
		config.EnvWorkOSOwnerID:  ownerSub,
	}
	accessClaims := func(email string) map[string]any {
		return map[string]any{"iss": issuer, "aud": []string{audTag}, "email": email, "sub": "cf-user-id", "type": "app"}
	}
	workosClaims := func(sub string) map[string]any {
		return map[string]any{"iss": issuer, "aud": resource, "sub": sub}
	}
	accessReq := func(r *http.Request, tok string) { r.Header.Set(gate.AccessHeader, tok) }
	workosReq := func(r *http.Request, tok string) { r.Header.Set("Authorization", "Bearer "+tok) }

	tests := []struct {
		name   string
		env    map[string]string // provider variables; nil leaves the provider off
		claims map[string]any
		attach func(*http.Request, string)
		admit  bool
	}{
		{"access owner", accessEnv, accessClaims(owner), accessReq, true},
		{"access non-owner", accessEnv, accessClaims("someone@example.com"), accessReq, false},
		{"access off", nil, accessClaims(owner), accessReq, false},
		{"workos owner", workosEnv, workosClaims(ownerSub), workosReq, true},
		{"workos non-owner", workosEnv, workosClaims("user_01OTHER"), workosReq, false},
		{"workos off", nil, workosClaims(ownerSub), workosReq, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			key, client := fakeProvider(t)
			env := map[string]string{"HOME": t.TempDir(), config.EnvToken: testToken}
			for k, v := range tc.env {
				env[k] = v
			}
			cfg, err := config.LoadServe(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
			if err != nil {
				t.Fatal(err)
			}
			protect, _, err := protection(cfg, client, slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatal(err)
			}
			reached := false
			h := protect(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusNoContent)
			}))

			now := time.Now()
			claims := map[string]any{"iat": now.Unix(), "exp": now.Add(time.Hour).Unix()}
			for k, v := range tc.claims {
				claims[k] = v
			}
			req := httptest.NewRequest(http.MethodPost, "/api/mcp", nil)
			tc.attach(req, signRS256(t, key, claims))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if reached != tc.admit {
				t.Fatalf("handler reached = %v, want %v (status %d, body %q)", reached, tc.admit, rec.Code, rec.Body.String())
			}
			want := http.StatusUnauthorized
			if tc.admit {
				want = http.StatusNoContent
			}
			if rec.Code != want {
				t.Fatalf("status = %d, want %d", rec.Code, want)
			}
		})
	}
}

// fakeProvider serves, as https://example.com, RFC 8414 metadata, a JWKS at
// /jwks and at Access's certs path, for the life of t. It returns the private
// key behind the published JWKS and a client that reaches the provider.
func fakeProvider(t *testing.T) (*rsa.PrivateKey, *http.Client) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: string(jose.RS256), Use: "sig"}}}
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(v); err != nil {
			t.Errorf("write json: %v", err)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"issuer": "https://" + providerHost, "jwks_uri": "https://" + providerHost + "/jwks"})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, jwks) })
	mux.HandleFunc("GET /cdn-cgi/access/certs", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, jwks) })
	srv := httptest.NewTLSServer(mux)

	tr := srv.Client().Transport.(*http.Transport).Clone()
	addr := srv.Listener.Addr().String()
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	t.Cleanup(func() {
		tr.CloseIdleConnections()
		srv.Close()
	})
	return key, &http.Client{Transport: tr}
}

func signRS256(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
