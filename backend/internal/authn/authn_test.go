package authn

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const (
	resource   = "https://wenna.test/api/mcp"
	audTag     = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	ownerEmail = "Owner@Example.com"
	ownerSub   = "user_01OWNER"
)

// fakeClock is a settable clock for token times and key refresh.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// syncBuffer is a log sink safe to read while a background fetch writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// provider is a fake OAuth provider over TLS: RFC 8414 metadata, a JWKS at
// /jwks and at Access's certs path, keys a test can rotate. It counts every
// request and JWKS fetches separately.
type provider struct {
	srv      *httptest.Server
	mu       sync.Mutex
	public   []jose.JSONWebKey
	meta     func(url string) map[string]any // overrides the metadata body
	jwksBody []byte                          // overrides the JWKS body
	redirect string                          // where /redirect sends
	gate     chan struct{}                   // when set, JWKS answers only after it closes
	status   atomic.Int64                    // JWKS status when non-zero
	requests atomic.Int64
	fetches  atomic.Int64
}

func newProvider(t *testing.T) *provider {
	t.Helper()
	p := &provider{}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		p.mu.Lock()
		meta := p.meta
		p.mu.Unlock()
		body := map[string]any{"issuer": p.url(), "jwks_uri": p.url() + "/jwks"}
		if meta != nil {
			body = meta(p.url())
		}
		writeJSON(t, w, body)
	})
	jwks := func(w http.ResponseWriter, r *http.Request) {
		p.fetches.Add(1)
		p.mu.Lock()
		keys, gate, body := p.public, p.gate, p.jwksBody
		p.mu.Unlock()
		if gate != nil {
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		if code := p.status.Load(); code != 0 {
			w.WriteHeader(int(code))
		}
		if body != nil {
			if _, err := w.Write(body); err != nil {
				t.Logf("write jwks: %v", err)
			}
			return
		}
		writeJSON(t, w, jose.JSONWebKeySet{Keys: keys})
	}
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		to := p.redirect
		p.mu.Unlock()
		http.Redirect(w, r, to, http.StatusFound)
	})
	mux.HandleFunc("/jwks", jwks)
	mux.HandleFunc(accessCertsPath, jwks)
	p.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.requests.Add(1)
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *provider) url() string { return p.srv.URL }

func (p *provider) set(f func(p *provider)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f(p)
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Logf("write json: %v", err)
	}
}

// signingKey is a private key and the kid it publishes under.
type signingKey struct {
	kid  string
	priv any
	alg  jose.SignatureAlgorithm
}

func newRSAKey(t *testing.T, kid string) signingKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return signingKey{kid: kid, priv: k, alg: jose.RS256}
}

func newECKey(t *testing.T, kid string) signingKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return signingKey{kid: kid, priv: k, alg: jose.ES256}
}

func (k signingKey) public() jose.JSONWebKey {
	var pub any
	switch p := k.priv.(type) {
	case *rsa.PrivateKey:
		pub = &p.PublicKey
	case *ecdsa.PrivateKey:
		pub = &p.PublicKey
	}
	return jose.JSONWebKey{Key: pub, KeyID: k.kid, Algorithm: string(k.alg), Use: "sig"}
}

func (p *provider) publish(keys ...signingKey) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.public = nil
	for _, k := range keys {
		p.public = append(p.public, k.public())
	}
}

func sign(t *testing.T, k signingKey, claims map[string]any) string {
	t.Helper()
	opts := (&jose.SignerOptions{}).WithType("JWT")
	if k.kid != "" {
		opts = opts.WithHeader("kid", k.kid)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: k.alg, Key: k.priv}, opts)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type kind int

const (
	access kind = iota
	workos
)

func (k kind) String() string {
	if k == access {
		return "access"
	}
	return "workos"
}

type harness struct {
	kind  kind
	p     *provider
	key   signingKey
	clock *fakeClock
	v     *Verifier
	logs  *syncBuffer
}

func newHarness(t *testing.T, k kind) *harness {
	t.Helper()
	p := newProvider(t)
	key := newRSAKey(t, "k1")
	p.publish(key)
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	var (
		v   *Verifier
		err error
	)
	if k == access {
		v, err = NewAccess(Access{TeamDomain: strings.TrimPrefix(p.url(), "https://"), Audience: audTag, OwnerEmail: ownerEmail}, p.srv.Client(), clock.Now, logger)
	} else {
		v, err = NewWorkOS(WorkOS{Issuer: p.url(), Resource: resource, OwnerSubject: ownerSub}, p.srv.Client(), clock.Now, logger)
	}
	if err != nil {
		t.Fatalf("new %s: %v", k, err)
	}
	return &harness{kind: k, p: p, key: key, clock: clock, v: v, logs: logs}
}

// claims are the defaults of a valid owner token; tests override fields.
func (h *harness) claims() map[string]any {
	now := h.clock.Now()
	c := map[string]any{
		"iss": h.p.url(),
		"iat": now.Unix(),
		"exp": now.Add(time.Hour).Unix(),
	}
	if h.kind == access {
		c["aud"] = []string{audTag}
		c["email"] = "owner@example.com" // case differs from the config on purpose
		c["sub"] = "cf-user-id"
		c["type"] = "app"
	} else {
		c["aud"] = resource
		c["sub"] = ownerSub
	}
	return c
}

func (h *harness) verify(t *testing.T, raw string) error {
	t.Helper()
	return h.v.Verify(context.Background(), raw)
}

// ks returns the verifier's key set; idle waits for any fetch to finish.
func (h *harness) ks() *keySet { return h.v.keys }

func idle(k *keySet) {
	k.mu.Lock()
	ch := k.inflight
	k.mu.Unlock()
	if ch != nil {
		<-ch
	}
}

var kinds = []kind{access, workos}

func TestOwnerTokenAccepted(t *testing.T) {
	t.Parallel()
	for _, k := range kinds {
		t.Run(k.String(), func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, k)
			if err := h.verify(t, sign(t, h.key, h.claims())); err != nil {
				t.Fatalf("owner token: %v; log %s", err, h.logs.String())
			}
			// aud as an array containing the audience among others.
			c := h.claims()
			c["aud"] = []string{"https://other.test", h.aud()}
			if err := h.verify(t, sign(t, h.key, c)); err != nil {
				t.Errorf("aud array: %v", err)
			}
			// nbf in the past is fine.
			c = h.claims()
			c["nbf"] = h.clock.Now().Add(-time.Minute).Unix()
			if err := h.verify(t, sign(t, h.key, c)); err != nil {
				t.Errorf("nbf in the past: %v", err)
			}
		})
	}
}

// aud is the audience a harness's tokens must carry.
func (h *harness) aud() string {
	if h.kind == access {
		return audTag
	}
	return resource
}

func TestAccessOwnerEmailIgnoresCase(t *testing.T) {
	t.Parallel()
	h := newHarness(t, access)
	for _, e := range []string{"owner@example.com", "OWNER@EXAMPLE.COM", ownerEmail} {
		c := h.claims()
		c["email"] = e
		if err := h.verify(t, sign(t, h.key, c)); err != nil {
			t.Errorf("email %q: %v", e, err)
		}
	}
}

func TestES256Accepted(t *testing.T) {
	t.Parallel()
	for _, k := range kinds {
		t.Run(k.String(), func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, k)
			ec := newECKey(t, "ec1")
			h.p.publish(h.key, ec)
			if err := h.verify(t, sign(t, ec, h.claims())); err != nil {
				t.Fatalf("ES256 token: %v; log %s", err, h.logs.String())
			}
		})
	}
}

// TestRefused: every token that must not authenticate is refused, and the
// error never carries the token.
// sameEmail folds ASCII case only: Unicode lookalikes that strings.EqualFold
// would fold (KELVIN SIGN, LONG S) are a different address.
func TestSameEmail(t *testing.T) {
	t.Parallel()
	tests := []struct {
		a, b string
		want bool
	}{
		{"kyle@example.com", "kyle@example.com", true},
		{"Kyle@Example.COM", "kyle@example.com", true},
		{"Kyle@example.com", "kyle@example.com", false}, // KELVIN SIGN
		{"kyle@Kelvin.example", "kyle@kelvin.example", false},
		{"ſam@example.com", "sam@example.com", false}, // LONG S
		{"josé@example.com", "josé@example.com", true},
		{"josÉ@example.com", "josé@example.com", false},
		{"kyle@example.com", "kyle@example.co", false},
		{"kyle@example.con", "kyle@example.com", false},
		{"", "", true},
	}
	for _, tc := range tests {
		if got := sameEmail(tc.a, tc.b); got != tc.want {
			t.Errorf("sameEmail(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
		if got := sameEmail(tc.b, tc.a); got != tc.want {
			t.Errorf("sameEmail(%q, %q) = %v, want %v", tc.b, tc.a, got, tc.want)
		}
	}
}

func TestRefused(t *testing.T) {
	t.Parallel()
	for _, k := range kinds {
		t.Run(k.String(), func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, k)
			// Load the keys first, so the cases below run against a warm cache.
			if err := h.verify(t, sign(t, h.key, h.claims())); err != nil {
				t.Fatalf("warm-up: %v", err)
			}
			stranger := newRSAKey(t, "k1") // same kid as the real key, different key

			with := func(mut func(map[string]any)) string {
				c := h.claims()
				mut(c)
				return sign(t, h.key, c)
			}
			valid := sign(t, h.key, h.claims())
			parts := strings.Split(valid, ".")
			b64 := base64.RawURLEncoding.EncodeToString

			pubDER, err := x509.MarshalPKIXPublicKey(&h.key.priv.(*rsa.PrivateKey).PublicKey)
			if err != nil {
				t.Fatal(err)
			}
			hs, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: pubDER}, (&jose.SignerOptions{}).WithHeader("kid", "k1"))
			if err != nil {
				t.Fatal(err)
			}
			hs256, err := jwt.Signed(hs).Claims(h.claims()).Serialize()
			if err != nil {
				t.Fatal(err)
			}
			now := h.clock.Now()

			tests := map[string]string{
				"empty":             "",
				"garbage":           "not-a-jwt",
				"expired":           with(func(c map[string]any) { c["exp"] = now.Add(-time.Minute).Unix() }),
				"no exp":            with(func(c map[string]any) { delete(c, "exp") }),
				"not yet valid":     with(func(c map[string]any) { c["nbf"] = now.Add(time.Hour).Unix() }),
				"wrong audience":    with(func(c map[string]any) { c["aud"] = "https://other.test/api/mcp" }),
				"no audience":       with(func(c map[string]any) { delete(c, "aud") }),
				"audience prefix":   with(func(c map[string]any) { c["aud"] = h.aud() + "x" }),
				"wrong issuer":      with(func(c map[string]any) { c["iss"] = "https://evil.test" }),
				"issuer trailing /": with(func(c map[string]any) { c["iss"] = h.p.url() + "/" }),
				"no issuer":         with(func(c map[string]any) { delete(c, "iss") }),
				"bad signature":     sign(t, stranger, h.claims()),
				"tampered payload":  parts[0] + "." + b64([]byte(`{"iss":"`+h.p.url()+`","sub":"`+ownerSub+`","email":"owner@example.com","aud":"`+h.aud()+`","exp":9999999999}`)) + "." + parts[2],
				"alg none":          b64([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + parts[1] + ".",
				"HS256 with public": hs256,
			}
			if k == access {
				tests["other email"] = with(func(c map[string]any) { c["email"] = "someone@example.com" })
				tests["no email"] = with(func(c map[string]any) { delete(c, "email") })
				tests["email suffix"] = with(func(c map[string]any) { c["email"] = "owner@example.com.evil.test" })
			} else {
				tests["other subject"] = with(func(c map[string]any) { c["sub"] = "user_01OTHER" })
				tests["no subject"] = with(func(c map[string]any) { delete(c, "sub") })
				tests["subject case"] = with(func(c map[string]any) { c["sub"] = strings.ToLower(ownerSub) })
			}
			for name, raw := range tests {
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					err := h.verify(t, raw)
					if err == nil {
						t.Fatal("token accepted")
					}
					if p := strings.Split(raw, "."); len(raw) > 0 && strings.Contains(err.Error(), p[len(p)-1]) && p[len(p)-1] != "" {
						t.Errorf("error carries the token: %v", err)
					}
				})
			}
		})
	}
}

func TestZeroVerifierRefuses(t *testing.T) {
	t.Parallel()
	var v Verifier
	if err := v.Verify(context.Background(), "x.y.z"); err == nil {
		t.Fatal("zero Verifier accepted a token")
	}
	var nilV *Verifier
	if err := nilV.Verify(context.Background(), "x.y.z"); err == nil {
		t.Fatal("nil Verifier accepted a token")
	}
	// Claims matching a zero Verifier's empty issuer and audience.
	raw := sign(t, newECKey(t, "k"), map[string]any{"iss": "", "aud": []string{""}, "exp": time.Now().Add(time.Hour).Unix()})
	if err := v.Verify(context.Background(), raw); err == nil {
		t.Fatal("zero Verifier accepted a token with empty issuer and audience")
	}
}

// TestConstructorsMakeNoRequest: a provider outage at boot cannot fail
// construction, because construction never calls the provider.
func TestConstructorsMakeNoRequest(t *testing.T) {
	t.Parallel()
	p := newProvider(t)
	p.publish(newRSAKey(t, "k1"))
	discard := slog.New(slog.DiscardHandler)
	if _, err := NewAccess(Access{TeamDomain: strings.TrimPrefix(p.url(), "https://"), Audience: audTag, OwnerEmail: ownerEmail}, p.srv.Client(), time.Now, discard); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWorkOS(WorkOS{Issuer: p.url(), Resource: resource, OwnerSubject: ownerSub}, p.srv.Client(), time.Now, discard); err != nil {
		t.Fatal(err)
	}
	// Against a provider that is down, construction still succeeds.
	down := httptest.NewTLSServer(http.NotFoundHandler())
	down.Close()
	if _, err := NewWorkOS(WorkOS{Issuer: down.URL, Resource: resource, OwnerSubject: ownerSub}, down.Client(), time.Now, discard); err != nil {
		t.Fatalf("NewWorkOS against a down provider: %v", err)
	}
	if got := p.requests.Load(); got != 0 {
		t.Fatalf("constructors made %d requests, want 0", got)
	}
}

func TestConstructorsRefuseMisconfiguration(t *testing.T) {
	t.Parallel()
	c := http.DefaultClient
	discard := slog.New(slog.DiscardHandler)
	okA := Access{TeamDomain: "team.cloudflareaccess.com", Audience: audTag, OwnerEmail: ownerEmail}
	okW := WorkOS{Issuer: "https://x.authkit.app", Resource: resource, OwnerSubject: ownerSub}
	if _, err := NewAccess(okA, c, time.Now, discard); err != nil {
		t.Fatalf("valid Access: %v", err)
	}
	if _, err := NewWorkOS(okW, c, time.Now, discard); err != nil {
		t.Fatalf("valid WorkOS: %v", err)
	}
	access := map[string]func(*Access){
		"empty team":         func(a *Access) { a.TeamDomain = "" },
		"team with scheme":   func(a *Access) { a.TeamDomain = "https://team.cloudflareaccess.com" },
		"team with path":     func(a *Access) { a.TeamDomain = "team.cloudflareaccess.com/x" },
		"team with query":    func(a *Access) { a.TeamDomain = "team.cloudflareaccess.com?x" },
		"team with userinfo": func(a *Access) { a.TeamDomain = "u@team.cloudflareaccess.com" },
		"empty audience":     func(a *Access) { a.Audience = " " },
		"empty owner":        func(a *Access) { a.OwnerEmail = "" },
		"owner not email":    func(a *Access) { a.OwnerEmail = "owner" },
	}
	for name, mut := range access {
		t.Run("access/"+name, func(t *testing.T) {
			t.Parallel()
			a := okA
			mut(&a)
			if _, err := NewAccess(a, c, time.Now, discard); err == nil {
				t.Fatalf("NewAccess(%+v) accepted", a)
			}
		})
	}
	workos := map[string]func(*WorkOS){
		"empty issuer":    func(w *WorkOS) { w.Issuer = "" },
		"http issuer":     func(w *WorkOS) { w.Issuer = "http://x.authkit.app" },
		"issuer no host":  func(w *WorkOS) { w.Issuer = "https:///x" },
		"issuer query":    func(w *WorkOS) { w.Issuer = "https://x.authkit.app?x=1" },
		"issuer fragment": func(w *WorkOS) { w.Issuer = "https://x.authkit.app#x" },
		"issuer creds":    func(w *WorkOS) { w.Issuer = "https://u:p@x.authkit.app" },
		"empty resource":  func(w *WorkOS) { w.Resource = "" },
		"empty owner":     func(w *WorkOS) { w.OwnerSubject = " " },
		"unparseable":     func(w *WorkOS) { w.Issuer = "https://x.authkit.app/%zz" },
	}
	for name, mut := range workos {
		t.Run("workos/"+name, func(t *testing.T) {
			t.Parallel()
			w := okW
			mut(&w)
			if _, err := NewWorkOS(w, c, time.Now, discard); err == nil {
				t.Fatalf("NewWorkOS(%+v) accepted", w)
			}
		})
	}
	t.Run("nil deps", func(t *testing.T) {
		t.Parallel()
		if _, err := NewAccess(okA, nil, time.Now, discard); err == nil {
			t.Error("nil client accepted")
		}
		if _, err := NewWorkOS(okW, c, nil, discard); err == nil {
			t.Error("nil clock accepted")
		}
		if _, err := NewWorkOS(okW, c, time.Now, nil); err == nil {
			t.Error("nil logger accepted")
		}
	})
}

func TestMetadataURL(t *testing.T) {
	t.Parallel()
	for issuer, want := range map[string]string{
		"https://x.authkit.app":         "https://x.authkit.app/.well-known/oauth-authorization-server",
		"https://x.authkit.app/":        "https://x.authkit.app/.well-known/oauth-authorization-server",
		"https://idp.test/tenant/a":     "https://idp.test/.well-known/oauth-authorization-server/tenant/a",
		"https://idp.test:8443/tenant/": "https://idp.test:8443/.well-known/oauth-authorization-server/tenant",
	} {
		got, err := metadataURL(issuer)
		if err != nil || got != want {
			t.Errorf("metadataURL(%q) = %q, %v; want %q", issuer, got, err, want)
		}
	}
}

// TestRefusedClaimsCostNoFetch: a forged token failing the issuer, audience
// or expiry check never reaches the key set, even on a cold cache.
func TestRefusedClaimsCostNoFetch(t *testing.T) {
	t.Parallel()
	for _, k := range kinds {
		t.Run(k.String(), func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, k)
			forged := newECKey(t, "forged")
			for _, mut := range []func(map[string]any){
				func(c map[string]any) { c["iss"] = "https://evil.test" },
				func(c map[string]any) { c["aud"] = "https://evil.test" },
				func(c map[string]any) { c["exp"] = h.clock.Now().Add(-time.Hour).Unix() },
				func(c map[string]any) { delete(c, "exp") },
			} {
				c := h.claims()
				mut(c)
				if err := h.verify(t, sign(t, forged, c)); err == nil {
					t.Fatal("forged token accepted")
				}
			}
			for _, raw := range []string{"garbage", "a.b.c", ""} {
				if err := h.verify(t, raw); err == nil {
					t.Fatalf("%q accepted", raw)
				}
			}
			if got := h.p.requests.Load(); got != 0 {
				t.Fatalf("refused tokens made %d provider requests, want 0", got)
			}
		})
	}
}

// TestUnknownKid: the first unknown kid after the window refetches once;
// repeated unknown kids inside the window do not refetch.
func TestUnknownKid(t *testing.T) {
	t.Parallel()
	for _, k := range kinds {
		t.Run(k.String(), func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, k)
			if err := h.verify(t, sign(t, h.key, h.claims())); err != nil {
				t.Fatalf("first token: %v", err)
			}
			if got := h.p.fetches.Load(); got != 1 {
				t.Fatalf("first use fetched JWKS %d times, want 1", got)
			}
			k2 := newRSAKey(t, "k2")
			h.p.publish(h.key, k2)

			for range 20 {
				if err := h.verify(t, sign(t, k2, h.claims())); err == nil {
					t.Fatal("new key accepted inside the window")
				}
			}
			if got := h.p.fetches.Load(); got != 1 {
				t.Fatalf("unknown kids inside the window: %d fetches, want 1", got)
			}

			h.clock.Advance(minRefresh)
			if err := h.verify(t, sign(t, k2, h.claims())); err != nil {
				t.Fatalf("new key after the window: %v; log %s", err, h.logs.String())
			}
			if got := h.p.fetches.Load(); got != 2 {
				t.Fatalf("after the window: %d fetches, want 2", got)
			}
			// A known key never refetches.
			h.clock.Advance(minRefresh)
			if err := h.verify(t, sign(t, h.key, h.claims())); err != nil {
				t.Fatalf("old key: %v", err)
			}
			if got := h.p.fetches.Load(); got != 2 {
				t.Fatalf("known key caused a fetch: %d", got)
			}
		})
	}
}

// TestUnknownKidFloodFetchesOnce: concurrent forged tokens naming fresh kids
// cost at most one JWKS fetch per window.
func TestUnknownKidFloodFetchesOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t, workos)
	if err := h.verify(t, sign(t, h.key, h.claims())); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(minRefresh)
	before := h.p.fetches.Load()
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			k := newECKey(t, "forged-"+string(rune('a'+i)))
			if err := h.verify(t, sign(t, k, h.claims())); err == nil {
				t.Error("forged token accepted")
			}
		})
	}
	wg.Wait()
	if got := h.p.fetches.Load() - before; got != 1 {
		t.Fatalf("JWKS fetches = %d, want 1", got)
	}
}

// TestBadKeyResponses: a JWKS that is not usable loads no keys and says why.
func TestBadKeyResponses(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		body   string
		status int
		log    string
	}{
		"no keys":          {body: `{"keys":[]}`, log: "no public signing keys"},
		"not json":         {body: `<html>`, log: "decode"},
		"error status":     {body: `{}`, status: http.StatusInternalServerError, log: "status 500"},
		"private key only": {body: privateJWKS(t), log: "no public signing keys"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, access)
			h.p.set(func(p *provider) { p.jwksBody = []byte(tc.body) })
			h.p.status.Store(int64(tc.status))
			if err := h.verify(t, sign(t, h.key, h.claims())); err == nil {
				t.Fatal("token accepted without usable keys")
			}
			if !strings.Contains(h.logs.String(), tc.log) {
				t.Fatalf("log lacks %q: %s", tc.log, h.logs.String())
			}
		})
	}
}

// privateJWKS is a JWKS publishing the private half of a key.
func privateJWKS(t *testing.T) string {
	t.Helper()
	k := newRSAKey(t, "k1")
	b, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: k.priv, KeyID: k.kid, Algorithm: string(k.alg), Use: "sig"}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestNonObjectPayloadRefused: a signed payload that is not a claims object
// is refused before any key lookup.
func TestNonObjectPayloadRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t, workos)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: h.key.alg, Key: h.key.priv}, (&jose.SignerOptions{}).WithHeader("kid", h.key.kid))
	if err != nil {
		t.Fatal(err)
	}
	jws, err := signer.Sign([]byte(`["not","claims"]`))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jws.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	if err := h.verify(t, raw); err == nil {
		t.Fatal("non-object payload accepted")
	}
	if got := h.p.requests.Load(); got != 0 {
		t.Fatalf("refused token made %d provider requests, want 0", got)
	}
}

func TestOversizedKeyResponse(t *testing.T) {
	t.Parallel()
	h := newHarness(t, access)
	pad := strings.Repeat("x", maxBodyBytes)
	h.p.set(func(p *provider) { p.jwksBody = []byte(`{"keys":[],"pad":"` + pad + `"}`) })
	if err := h.verify(t, sign(t, h.key, h.claims())); err == nil {
		t.Fatal("token accepted against an oversized JWKS")
	}
	if !strings.Contains(h.logs.String(), "exceeds") {
		t.Fatalf("oversized JWKS not logged as such; log %s", h.logs.String())
	}
}

// TestKeyFetchTimeout: a provider that never answers costs one bounded wait.
func TestKeyFetchTimeout(t *testing.T) {
	t.Parallel()
	h := newHarness(t, access)
	gate := make(chan struct{})
	defer close(gate)
	h.p.set(func(p *provider) { p.gate = gate })
	h.ks().timeout = 50 * time.Millisecond // set before any fetch starts

	done := make(chan error, 1)
	go func() { done <- h.verify(t, sign(t, h.key, h.claims())) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("token accepted without keys")
		}
	case <-time.After(30 * time.Second): // hang guard: the fetch must time out first
		t.Fatal("Verify waited past the fetch timeout")
	}
	idle(h.ks())
	if !strings.Contains(h.logs.String(), "deadline exceeded") {
		t.Fatalf("timeout not logged; log %s", h.logs.String())
	}
}

// TestWorkOSDiscoveryRefused: metadata a token must not be trusted under
// loads no keys, and says why in the log.
func TestWorkOSDiscoveryRefused(t *testing.T) {
	t.Parallel()
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(plain.Close)
	for name, tc := range map[string]struct {
		meta func(h *harness, u string) map[string]any
		log  string
	}{
		"another issuer": {func(_ *harness, u string) map[string]any {
			return map[string]any{"issuer": "https://evil.test", "jwks_uri": u + "/jwks"}
		}, "must match exactly"},
		"issuer trailing slash": {func(_ *harness, u string) map[string]any {
			return map[string]any{"issuer": u + "/", "jwks_uri": u + "/jwks"}
		}, "must match exactly"},
		"no jwks_uri": {func(_ *harness, u string) map[string]any {
			return map[string]any{"issuer": u}
		}, "no jwks_uri"},
		"http jwks_uri": {func(_ *harness, u string) map[string]any {
			return map[string]any{"issuer": u, "jwks_uri": plain.URL + "/jwks"}
		}, "not https"},
		"redirect to http": {func(h *harness, u string) map[string]any {
			return map[string]any{"issuer": u, "jwks_uri": u + "/redirect"}
		}, "redirect refused"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, workos)
			h.p.set(func(p *provider) {
				p.meta = func(u string) map[string]any { return tc.meta(h, u) }
				p.redirect = plain.URL + "/jwks"
			})
			if err := h.verify(t, sign(t, h.key, h.claims())); err == nil {
				t.Fatal("token accepted")
			}
			if !strings.Contains(h.logs.String(), tc.log) {
				t.Fatalf("log lacks %q: %s", tc.log, h.logs.String())
			}
		})
	}
}

func TestProviderClientRedirects(t *testing.T) {
	t.Parallel()
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(target.Close)
	var to atomic.Value
	var hops atomic.Int64
	redirector := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A loop ends in 200 after 2*maxRedirects hops, so only the redirect
		// limit can refuse it (and a missing limit cannot hang the test).
		if hops.Add(1) > 2*maxRedirects {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, to.Load().(string), http.StatusFound)
	}))
	t.Cleanup(redirector.Close)
	// One client trusting both test certificates.
	client := redirector.Client()
	client.Transport.(*http.Transport).TLSClientConfig.RootCAs.AddCert(target.Certificate())

	for _, tc := range []struct {
		to     string
		wantOK bool
	}{
		{to: target.URL, wantOK: true},
		{to: strings.Replace(target.URL, "https://", "http://", 1)},
		{to: strings.Replace(target.URL, "https://", "https://u:p@", 1)},
		{to: redirector.URL}, // a loop: refused after maxRedirects
	} {
		to.Store(tc.to)
		hops.Store(0)
		res, err := providerClient(client).Get(redirector.URL)
		if err == nil {
			if cerr := res.Body.Close(); cerr != nil {
				t.Fatal(cerr)
			}
		}
		if (err == nil) != tc.wantOK {
			t.Errorf("redirect to %q: err = %v, want ok %v", tc.to, err, tc.wantOK)
		}
	}
}

// TestCancelledCallerDoesNotCancelFetch: a caller hanging up mid-fetch still
// lets the fetch complete, so a rotated key is learned.
func TestCancelledCallerDoesNotCancelFetch(t *testing.T) {
	t.Parallel()
	h := newHarness(t, workos)
	if err := h.verify(t, sign(t, h.key, h.claims())); err != nil {
		t.Fatal(err)
	}
	k2 := newRSAKey(t, "k2")
	h.p.publish(h.key, k2)
	gate := make(chan struct{})
	h.p.set(func(p *provider) { p.gate = gate })
	h.clock.Advance(minRefresh)

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	// The key set directly: go-oidc flattens the error chain.
	go func() {
		_, err := h.ks().VerifySignature(ctx, sign(t, newECKey(t, "forged"), h.claims()))
		errc <- err
	}()
	for h.p.fetches.Load() < 2 { // the first fetch + this one, now blocked on the gate
		runtime.Gosched()
	}
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled caller = %v, want context.Canceled", err)
	}
	close(gate)
	idle(h.ks())
	if err := h.verify(t, sign(t, k2, h.claims())); err != nil {
		t.Fatalf("rotated key after a cancelled caller: %v", err)
	}
}

// TestKnownKeyNeverWaitsOnFetch: while a fetch hangs, tokens signed by a
// cached key still verify.
func TestKnownKeyNeverWaitsOnFetch(t *testing.T) {
	t.Parallel()
	h := newHarness(t, access)
	if err := h.verify(t, sign(t, h.key, h.claims())); err != nil {
		t.Fatal(err)
	}
	h.ks().mu.Lock()
	h.ks().timeout = time.Hour // the fetch hangs until the gate opens
	h.ks().mu.Unlock()
	gate := make(chan struct{})
	h.p.set(func(p *provider) { p.gate = gate })
	defer close(gate)
	h.clock.Advance(minRefresh)

	go func() {
		if err := h.verify(t, sign(t, newECKey(t, "forged"), h.claims())); err == nil {
			t.Error("forged token accepted")
		}
	}()
	for h.p.fetches.Load() < 2 {
		runtime.Gosched()
	}
	done := make(chan error, 1)
	go func() { done <- h.verify(t, sign(t, h.key, h.claims())) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("known key during a hung fetch: %v", err)
		}
	case <-time.After(30 * time.Second): // hang guard
		t.Fatal("a known-key token waited on the hung fetch")
	}
}

// TestRevokedKeyStopsVerifying: once keys are older than maxKeyAge, use
// triggers a refresh; a key the provider dropped is then refused.
func TestRevokedKeyStopsVerifying(t *testing.T) {
	t.Parallel()
	h := newHarness(t, access)
	if err := h.verify(t, sign(t, h.key, h.claims())); err != nil {
		t.Fatal(err)
	}
	k2 := newRSAKey(t, "k2")
	h.p.publish(k2) // k1 revoked
	h.clock.Advance(maxKeyAge)

	// Stale-while-revalidate: this one still passes and starts the refresh.
	if err := h.verify(t, sign(t, h.key, h.claims())); err != nil {
		t.Fatalf("first use after maxKeyAge: %v", err)
	}
	idle(h.ks())
	if err := h.verify(t, sign(t, h.key, h.claims())); err == nil {
		t.Fatal("revoked key accepted after refresh")
	}
	if err := h.verify(t, sign(t, k2, h.claims())); err != nil {
		t.Fatalf("current key after refresh: %v", err)
	}
}

// TestKeyAlgorithmBinds: a key published for one algorithm does not verify
// another of the same key type.
func TestKeyAlgorithmBinds(t *testing.T) {
	t.Parallel()
	h := newHarness(t, workos)
	pub := h.key.public()
	pub.Algorithm = "PS256"
	h.p.set(func(p *provider) { p.public = []jose.JSONWebKey{pub} })
	if err := h.verify(t, sign(t, h.key, h.claims())); err == nil {
		t.Fatal("RS256 token accepted against a PS256 key")
	}
}

// TestRefreshFailure: a failing refresh is logged, keeps the cached keys and
// is throttled; once the provider recovers, the next window refreshes.
func TestRefreshFailure(t *testing.T) {
	t.Parallel()
	h := newHarness(t, workos)
	if err := h.verify(t, sign(t, h.key, h.claims())); err != nil {
		t.Fatal(err)
	}
	h.p.status.Store(http.StatusInternalServerError)
	h.clock.Advance(maxKeyAge)
	before := h.p.fetches.Load()

	if err := h.verify(t, sign(t, h.key, h.claims())); err != nil {
		t.Fatalf("cached key while refresh fails: %v", err)
	}
	idle(h.ks())
	if !strings.Contains(h.logs.String(), "refresh provider signing keys") {
		t.Errorf("failed refresh not logged; log %q", h.logs.String())
	}
	for range 10 {
		if err := h.verify(t, sign(t, h.key, h.claims())); err != nil {
			t.Fatalf("cached key: %v", err)
		}
		if err := h.verify(t, sign(t, newECKey(t, "forged"), h.claims())); err == nil {
			t.Fatal("forged token accepted")
		}
	}
	idle(h.ks())
	if got := h.p.fetches.Load() - before; got != 1 {
		t.Fatalf("fetches while failing, one window = %d, want 1", got)
	}

	k2 := newRSAKey(t, "k2")
	h.p.publish(k2)
	h.p.status.Store(0)
	h.clock.Advance(minRefresh)
	if err := h.verify(t, sign(t, h.key, h.claims())); err != nil {
		t.Fatalf("aged key before recovery refresh lands: %v", err)
	}
	idle(h.ks())
	if got := h.p.fetches.Load() - before; got != 2 {
		t.Fatalf("fetches after recovery = %d, want 2", got)
	}
	if err := h.verify(t, sign(t, h.key, h.claims())); err == nil {
		t.Fatal("revoked key accepted after recovery")
	}
}

// TestLogsCarryNoToken: nothing logged on any path names the token.
func TestLogsCarryNoToken(t *testing.T) {
	t.Parallel()
	h := newHarness(t, workos)
	h.p.status.Store(http.StatusInternalServerError)
	raw := sign(t, h.key, h.claims())
	if err := h.verify(t, raw); err == nil {
		t.Fatal("token accepted without keys")
	}
	idle(h.ks())
	sig := raw[strings.LastIndex(raw, ".")+1:]
	if logs := h.logs.String(); logs == "" || strings.Contains(logs, sig) {
		t.Fatalf("log empty or carries the token: %q", logs)
	}
}
