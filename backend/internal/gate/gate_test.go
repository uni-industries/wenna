package gate

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

const (
	token = "a-token-of-at-least-thirty-two-bytes-0123"
	// goodJWT and badJWT are JWT-shaped; the fakes accept only goodJWT.
	goodJWT = "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJvd25lciJ9.c2lnbmF0dXJl"
	badJWT  = "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJvdGhlciJ9.Zm9yZ2Vk"
	metaURL = "https://wenna.example/.well-known/oauth-protected-resource/api/mcp"
)

// fake accepts exactly good and records every value it was handed.
type fake struct {
	good string
	mu   sync.Mutex
	seen []string
}

func (f *fake) Verify(_ context.Context, raw string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, raw)
	if raw != f.good {
		return errors.New("signature mismatch")
	}
	return nil
}

func (f *fake) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

type result struct {
	code    int
	reached bool
	rec     *httptest.ResponseRecorder
	logs    string
}

func serve(o Options, header http.Header) result {
	var logs bytes.Buffer
	o.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	reached := false
	h := Require(o)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	req := httptest.NewRequest(http.MethodPost, "/api/mcp", nil)
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return result{code: rec.Code, reached: reached, rec: rec, logs: logs.String()}
}

func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i < len(kv); i += 2 {
		h.Add(kv[i], kv[i+1])
	}
	return h
}

func TestRequire(t *testing.T) {
	t.Parallel()
	long := "a." + strings.Repeat("b", maxCredential) + ".c"
	tests := []struct {
		name       string
		access     bool // Access verifier on
		workos     bool // WorkOS verifier on
		header     http.Header
		admit      bool
		wantAccess []string // values the Access verifier must have been handed
		wantWorkOS []string
	}{
		{name: "token", header: hdr("Authorization", "Bearer "+token), admit: true},
		{name: "token with both verifiers on, none called", access: true, workos: true,
			header: hdr("Authorization", "Bearer "+token, AccessHeader, goodJWT), admit: true},
		{name: "wrong token", header: hdr("Authorization", "Bearer "+token+"x")},
		{name: "no credentials", access: true, workos: true, header: hdr()},
		{name: "token in access header is not a token", access: true,
			header: hdr(AccessHeader, token), wantAccess: []string{token}},

		{name: "access valid", access: true, header: hdr(AccessHeader, goodJWT), admit: true, wantAccess: []string{goodJWT}},
		{name: "access invalid", access: true, header: hdr(AccessHeader, badJWT), wantAccess: []string{badJWT}},
		{name: "access valid beside wrong bearer", access: true,
			header: hdr("Authorization", "Bearer wrong", AccessHeader, goodJWT), admit: true, wantAccess: []string{goodJWT}},
		{name: "access off: header ignored", header: hdr(AccessHeader, goodJWT)},
		{name: "access header empty is not sent to verifier", access: true, header: hdr(AccessHeader, "")},
		{name: "access too long never verified", access: true, header: hdr(AccessHeader, long)},
		{name: "access same-origin admits", access: true,
			header: hdr(AccessHeader, goodJWT, "Sec-Fetch-Site", "same-origin"), admit: true, wantAccess: []string{goodJWT}},
		{name: "access user-initiated admits", access: true,
			header: hdr(AccessHeader, goodJWT, "Sec-Fetch-Site", "none"), admit: true, wantAccess: []string{goodJWT}},
		{name: "access cross-site refused before verify", access: true,
			header: hdr(AccessHeader, goodJWT, "Sec-Fetch-Site", "cross-site")},
		{name: "access same-site refused before verify", access: true,
			header: hdr(AccessHeader, goodJWT, "Sec-Fetch-Site", "same-site")},
		{name: "access origin without sec-fetch-site refused", access: true,
			header: hdr(AccessHeader, goodJWT, "Origin", "https://evil.example")},
		{name: "access same-origin beats origin", access: true,
			header: hdr(AccessHeader, goodJWT, "Sec-Fetch-Site", "same-origin", "Origin", "https://wenna.example"),
			admit:  true, wantAccess: []string{goodJWT}},
		{name: "cross-site does not block the token", access: true,
			header: hdr("Authorization", "Bearer "+token, AccessHeader, goodJWT, "Sec-Fetch-Site", "cross-site"), admit: true},
		{name: "cross-site does not block workos", access: true, workos: true,
			header: hdr("Authorization", "Bearer "+goodJWT, AccessHeader, goodJWT, "Sec-Fetch-Site", "cross-site"),
			admit:  true, wantWorkOS: []string{goodJWT}},

		{name: "workos valid", workos: true, header: hdr("Authorization", "Bearer "+goodJWT), admit: true, wantWorkOS: []string{goodJWT}},
		{name: "workos valid, lowercase scheme", workos: true, header: hdr("Authorization", "bearer "+goodJWT), admit: true, wantWorkOS: []string{goodJWT}},
		{name: "workos invalid", workos: true, header: hdr("Authorization", "Bearer "+badJWT), wantWorkOS: []string{badJWT}},
		{name: "workos off: jwt bearer ignored", header: hdr("Authorization", "Bearer "+goodJWT)},
		{name: "non-jwt bearer never reaches workos", workos: true, header: hdr("Authorization", "Bearer "+token+"x")},
		{name: "two parts never reach workos", workos: true, header: hdr("Authorization", "Bearer a.b")},
		{name: "four parts never reach workos", workos: true, header: hdr("Authorization", "Bearer a.b.c.d")},
		{name: "empty part never reaches workos", workos: true, header: hdr("Authorization", "Bearer a..c")},
		{name: "leading dot never reaches workos", workos: true, header: hdr("Authorization", "Bearer .b.c")},
		{name: "trailing dot never reaches workos", workos: true, header: hdr("Authorization", "Bearer a.b.")},
		{name: "basic scheme never reaches workos", workos: true, header: hdr("Authorization", "Basic "+goodJWT)},
		{name: "workos too long never verified", workos: true, header: hdr("Authorization", "Bearer "+long)},
		{name: "jwt in access header does not reach workos", workos: true, header: hdr(AccessHeader, goodJWT)},
		{name: "jwt bearer does not reach access", access: true, header: hdr("Authorization", "Bearer "+goodJWT)},

		{name: "access refused, workos admits", access: true, workos: true,
			header: hdr("Authorization", "Bearer "+goodJWT, AccessHeader, badJWT), admit: true,
			wantAccess: []string{badJWT}, wantWorkOS: []string{goodJWT}},
		{name: "access admits, workos not called", access: true, workos: true,
			header: hdr("Authorization", "Bearer "+badJWT, AccessHeader, goodJWT), admit: true,
			wantAccess: []string{goodJWT}},
		{name: "both refuse", access: true, workos: true,
			header:     hdr("Authorization", "Bearer "+badJWT, AccessHeader, badJWT),
			wantAccess: []string{badJWT}, wantWorkOS: []string{badJWT}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			access, workos := &fake{good: goodJWT}, &fake{good: goodJWT}
			o := Options{Token: token}
			if tc.access {
				o.Access = access
			}
			if tc.workos {
				o.WorkOS = workos
			}
			got := serve(o, tc.header)

			wantCode := http.StatusUnauthorized
			if tc.admit {
				wantCode = http.StatusOK
			}
			if got.code != wantCode {
				t.Errorf("status = %d, want %d", got.code, wantCode)
			}
			if got.reached != tc.admit {
				t.Errorf("wrapped handler reached = %v, want %v", got.reached, tc.admit)
			}
			if !tc.admit && got.rec.Header().Get("WWW-Authenticate") == "" {
				t.Error("401 without a WWW-Authenticate challenge")
			}
			if c := access.calls(); !slices.Equal(c, tc.wantAccess) {
				t.Errorf("Access verifier saw %q, want %q", c, tc.wantAccess)
			}
			if c := workos.calls(); !slices.Equal(c, tc.wantWorkOS) {
				t.Errorf("WorkOS verifier saw %q, want %q", c, tc.wantWorkOS)
			}
		})
	}
}

// An unset token must not be satisfied by an empty or absent bearer.
func TestRequireEmptyTokenAdmitsNoBearer(t *testing.T) {
	t.Parallel()
	for _, h := range []http.Header{hdr(), hdr("Authorization", "Bearer "), hdr("Authorization", "Bearer x")} {
		if got := serve(Options{}, h); got.code != http.StatusUnauthorized || got.reached {
			t.Errorf("header %v: status %d, reached %v; want 401, not reached", h, got.code, got.reached)
		}
	}
}

func TestChallenge(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, url, want string
	}{
		{"without metadata", "", `Bearer realm="wenna"`},
		{"with metadata", metaURL, `Bearer realm="wenna", resource_metadata="` + metaURL + `"`},
		// A quote in the configured value must not end the quoted-string early.
		{"metadata escaped", `https://x/"a\b`, `Bearer realm="wenna", resource_metadata="https://x/\"a\\b"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := serve(Options{Token: token, ResourceMetadataURL: tc.url}, hdr("Authorization", "Bearer nope"))
			if got.code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", got.code)
			}
			if h := got.rec.Header().Get("WWW-Authenticate"); h != tc.want {
				t.Errorf("WWW-Authenticate = %q, want %q", h, tc.want)
			}
			const want = "A valid token or sign-in is required."
			if body := strings.TrimSpace(got.rec.Body.String()); body != want {
				t.Errorf("body = %q, want %q", body, want)
			}
		})
	}
}

// A verifier's refusal is logged with its reason, never with the credential,
// and the reason never reaches the client.
func TestRefusalLoggedWithoutCredential(t *testing.T) {
	t.Parallel()
	got := serve(Options{Token: token, Access: &fake{good: goodJWT}, WorkOS: &fake{good: goodJWT}},
		hdr("Authorization", "Bearer "+badJWT, AccessHeader, badJWT+"x"))
	if got.code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", got.code)
	}
	if !strings.Contains(got.logs, "signature mismatch") {
		t.Errorf("logs = %q, want the verifier's reason", got.logs)
	}
	for _, v := range []string{"verifier=access", "verifier=workos", "level=INFO"} {
		if !strings.Contains(got.logs, v) {
			t.Errorf("logs = %q, want %q", got.logs, v)
		}
	}
	// Vacuity control: the logs are non-empty, so absence below means something.
	for _, secret := range []string{badJWT, "eyJ"} {
		if strings.Contains(got.logs, secret) {
			t.Errorf("logs carry credential material %q: %q", secret, got.logs)
		}
	}
	if strings.Contains(got.rec.Body.String(), "signature") {
		t.Errorf("body leaks the verifier's reason: %q", got.rec.Body.String())
	}
}

// An unauthenticated caller controls Sec-Fetch-Site; the log carries only a
// defined value, never the caller's text.
func TestCrossSiteLogBounded(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ site, want string }{
		{"cross-site", "sec_fetch_site=cross-site"},
		{"same-site", "sec_fetch_site=same-site"},
		{strings.Repeat("A", 64<<10), "sec_fetch_site=other"},
	} {
		got := serve(Options{Access: &fake{good: goodJWT}}, hdr(AccessHeader, goodJWT, "Sec-Fetch-Site", tc.site))
		if got.code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", got.code)
		}
		if !strings.Contains(got.logs, tc.want) {
			t.Errorf("logs = %.200q, want %q", got.logs, tc.want)
		}
		if len(got.logs) > 1024 {
			t.Errorf("log is %d bytes for one refusal", len(got.logs))
		}
	}
}

func TestRequireNilLoggerPanics(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("Require with a nil Logger did not panic")
		}
	}()
	Require(Options{Token: token})
}
