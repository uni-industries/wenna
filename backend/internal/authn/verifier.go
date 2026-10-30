// Package authn is the one implementation of "is this OAuth proof the
// owner's" (docs/controls.md): it verifies a token signed by the OAuth
// provider the user runs in front of wenna — Cloudflare Access or WorkOS
// AuthKit — and admits it only when it names the configured owner
// (CLAUDE.md § Decisions, Identity). Sign-in, consent and token issuance are
// the provider's; nothing here issues or stores identity.
//
// Constructors do no network I/O, so a provider outage at boot cannot stop
// the bearer route. Signing keys load on first use and are cached. A token
// with the wrong issuer, audience or expiry is refused before any key lookup;
// one naming an unknown key refetches keys at most once per minRefresh. A
// forged token costs a parse and a cached-key check, never an unbounded
// outbound fetch.
package authn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// Verifier admits one provider's tokens that name the owner.
type Verifier struct {
	provider string // for error context: "cloudflare access", "workos"
	issuer   string
	audience string
	now      func() time.Time
	tokens   *oidc.IDTokenVerifier
	keys     *keySet
	owner    func(*oidc.IDToken) error
}

// errNotOwner is a validly signed token naming someone other than the owner.
var errNotOwner = errors.New("token does not name the owner")

// newVerifier builds a Verifier for tokens from issuer for audience, signed by
// keys. It does no I/O.
func newVerifier(provider, issuer, audience string, keys *keySet, now func() time.Time, owner func(*oidc.IDToken) error) *Verifier {
	algs := make([]string, len(signingAlgs))
	for i, a := range signingAlgs {
		algs[i] = string(a)
	}
	return &Verifier{
		provider: provider,
		issuer:   issuer,
		audience: audience,
		now:      now,
		tokens: oidc.NewVerifier(issuer, keys, &oidc.Config{
			ClientID:             audience,
			SupportedSigningAlgs: algs,
			Now:                  now,
		}),
		keys:  keys,
		owner: owner,
	}
}

// Verify returns nil only for a validly signed, unexpired token from the
// configured issuer and audience that names the owner. The error never
// carries the token.
func (v *Verifier) Verify(ctx context.Context, raw string) error {
	if v == nil || v.tokens == nil {
		// A zero Verifier refuses every token rather than panicking.
		return errors.New("authn: verifier not configured")
	}
	if err := v.precheck(raw); err != nil {
		return fmt.Errorf("authn: %s token: %w", v.provider, err)
	}
	tok, err := v.tokens.Verify(ctx, raw)
	if err != nil {
		return fmt.Errorf("authn: %s token: %w", v.provider, err)
	}
	if err := v.owner(tok); err != nil {
		return fmt.Errorf("authn: %s token: %w", v.provider, err)
	}
	return nil
}

// precheck refuses, from the unverified payload, a token go-oidc would
// refuse for its issuer, audience or expiry. go-oidc checks the signature
// first, so without this a forged token naming an unknown key could start a
// key fetch whatever its claims. It only ever refuses; go-oidc's checks stay
// the control.
func (v *Verifier) precheck(raw string) error {
	// The parse errors are not wrapped: they quote the unverified token, which
	// anyone can fill with text the log would then carry.
	jws, err := jose.ParseSigned(raw, signingAlgs)
	if err != nil {
		return errors.New("token is malformed or uses a refused algorithm")
	}
	var c jwt.Claims
	if err := json.Unmarshal(jws.UnsafePayloadWithoutVerification(), &c); err != nil {
		return errors.New("token claims are malformed")
	}
	switch {
	case c.Issuer != v.issuer:
		return errors.New("token is from another issuer")
	case !c.Audience.Contains(v.audience):
		return errors.New("token is for another audience")
	case c.Expiry == nil || c.Expiry.Time().Before(v.now()):
		return errors.New("token has no expiry or has expired")
	}
	return nil
}

// check validates the shared constructor arguments.
func check(client *http.Client, now func() time.Time, logger *slog.Logger) error {
	switch {
	case client == nil:
		return errors.New("authn: nil http client")
	case now == nil:
		return errors.New("authn: nil clock")
	case logger == nil:
		return errors.New("authn: nil logger")
	}
	return nil
}
