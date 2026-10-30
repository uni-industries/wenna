// Package gate is the one authentication of the HTTP server
// (docs/controls.md): it admits a request only when it carries proof that it
// is the instance's owner. Proofs, cheapest first:
//
//  1. the configured token as a bearer token, compared in constant time;
//  2. when a Cloudflare Access verifier is set, a valid Cf-Access-Jwt-Assertion;
//  3. when a WorkOS verifier is set, a bearer value shaped like a JWT that it
//     accepts.
//
// Nothing else about a request — host, path, address — counts as proof.
package gate

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/uni-industries/wenna/backend/internal/bearer"
)

// AccessHeader carries the Cloudflare Access assertion.
const AccessHeader = "Cf-Access-Jwt-Assertion"

// maxCredential bounds what a verifier is handed: a longer value is refused
// before any parsing or signature work. Real access tokens are a few KiB.
const maxCredential = 16 << 10

// refusal is the body of every 401: one plain sentence.
const refusal = "A valid token or sign-in is required."

// Verifier checks a signed credential. A nil error means it proves the owner.
type Verifier interface {
	Verify(ctx context.Context, raw string) error
}

// Options configures Require.
type Options struct {
	// Token is WENNA_TOKEN. Empty means no token is accepted.
	Token string
	// Access verifies the Cloudflare Access header; nil turns it off.
	Access Verifier
	// WorkOS verifies JWT-shaped bearer tokens; nil turns it off.
	WorkOS Verifier
	// ResourceMetadataURL, when set, is advertised in every 401 challenge
	// (RFC 9728 § 5.1) so an MCP client can start OAuth from it. Set it only
	// when an OAuth verifier is on.
	ResourceMetadataURL string
	// Logger records why a verifier refused; never the credential.
	Logger *slog.Logger
}

// Require returns middleware that refuses, with 401, any request without the
// owner's proof. It runs before the wrapped handler, so a refused request
// costs no store work.
// It panics on a nil Logger: a wiring bug, caught at startup.
func Require(o Options) func(http.Handler) http.Handler {
	if o.Logger == nil {
		panic("gate.Require: nil Logger")
	}
	tokenMatches := bearer.Matches(o.Token)
	challenge := `Bearer realm="wenna"`
	if o.ResourceMetadataURL != "" {
		challenge += `, resource_metadata=` + quote(o.ResourceMetadataURL)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tokenMatches(r) || o.verified(r) {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("WWW-Authenticate", challenge)
			http.Error(w, refusal, http.StatusUnauthorized)
		})
	}
}

// verified tries the configured verifiers in order.
func (o Options) verified(r *http.Request) bool {
	if o.Access != nil {
		if raw := r.Header.Get(AccessHeader); raw != "" && o.notCrossSite(r) && o.accepts(r, "access", o.Access, raw) {
			return true
		}
	}
	if o.WorkOS != nil {
		if raw, ok := bearer.FromHeader(r.Header.Get("Authorization")); ok && jwtShaped(raw) &&
			o.accepts(r, "workos", o.WorkOS, raw) {
			return true
		}
	}
	return false
}

// notCrossSite guards the Access proof against cross-site request forgery
// (CLAUDE.md § Security invariants). Cloudflare adds the Access header to any
// request carrying the browser's Access cookie, so unlike a bearer token a
// browser attaches it on its own. A browser marks every request with
// Sec-Fetch-Site, or failing that with Origin on a cross-origin one: only
// same-origin or user-initiated ("none") passes, and a bare Origin is
// refused, since no configured origin can vouch for it. A request with
// neither header did not come from a browser page (an MCP client calling
// through Access) and has no cross-site page to forge it.
func (o Options) notCrossSite(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		if site == "same-origin" || site == "none" {
			return true
		}
		// Logged only as one of the values the header is defined to take: the
		// raw header is the caller's text, up to the server's header limit.
		if site != "same-site" && site != "cross-site" {
			site = "other"
		}
		o.Logger.InfoContext(r.Context(), "credential refused", "verifier", "access", "reason", "cross-site", "sec_fetch_site", site)
		return false
	}
	if r.Header.Get("Origin") != "" {
		o.Logger.InfoContext(r.Context(), "credential refused", "verifier", "access", "reason", "origin without sec-fetch-site")
		return false
	}
	return true
}

// accepts runs v on raw and logs a refusal without the credential.
func (o Options) accepts(r *http.Request, name string, v Verifier, raw string) bool {
	if len(raw) > maxCredential {
		o.Logger.InfoContext(r.Context(), "credential refused", "verifier", name, "reason", "too long", "len", len(raw))
		return false
	}
	if err := v.Verify(r.Context(), raw); err != nil {
		o.Logger.InfoContext(r.Context(), "credential refused", "verifier", name, "err", err)
		return false
	}
	return true
}

// jwtShaped reports whether s is three non-empty dot-separated parts, the
// compact JWS form. Anything else never reaches a JWT verifier.
func jwtShaped(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
	}
	return true
}

// quote renders s as an RFC 9110 quoted-string.
func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
