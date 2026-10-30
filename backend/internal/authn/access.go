package authn

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// Access configures the Cloudflare Access verifier. TeamDomain is a bare host
// (the team's Access domain, no scheme or path); Audience is the Access
// application's AUD tag; OwnerEmail is the owner's email, compared
// case-insensitively. Access forwards the token in Cf-Access-Jwt-Assertion.
type Access struct{ TeamDomain, Audience, OwnerEmail string }

// accessCertsPath is where Access publishes a team's signing keys.
const accessCertsPath = "/cdn-cgi/access/certs"

// NewAccess returns a Verifier for Cloudflare Access application tokens. It
// does no network I/O; it refuses only misconfiguration detectable offline.
func NewAccess(c Access, client *http.Client, now func() time.Time, logger *slog.Logger) (*Verifier, error) {
	if err := check(client, now, logger); err != nil {
		return nil, err
	}
	issuer, err := accessIssuer(c.TeamDomain)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(c.Audience) == "" {
		return nil, errors.New("authn: cloudflare access: empty audience tag")
	}
	owner := strings.TrimSpace(c.OwnerEmail)
	if owner == "" || !strings.Contains(owner, "@") {
		return nil, errors.New("authn: cloudflare access: owner email is not an email address")
	}
	certs := issuer + accessCertsPath
	keys := newKeySet(func(context.Context, *http.Client) (string, error) { return certs, nil }, client, now, logger)
	return newVerifier("cloudflare access", issuer, c.Audience, keys, now, func(tok *oidc.IDToken) error {
		var claims struct {
			Email string `json:"email"`
		}
		if err := tok.Claims(&claims); err != nil {
			return fmt.Errorf("read claims: %w", err)
		}
		if claims.Email == "" || !sameEmail(claims.Email, owner) {
			return errNotOwner
		}
		return nil
	}), nil
}

// sameEmail compares two addresses folding ASCII case only. strings.EqualFold
// folds Unicode too (KELVIN SIGN matches k), so a lookalike address the
// provider treats as distinct would pass as the owner.
func sameEmail(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if x >= 0x80 || y >= 0x80 {
			if x != y {
				return false
			}
			continue
		}
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

// accessIssuer returns https://<team domain>, refusing anything but a bare
// host.
func accessIssuer(team string) (string, error) {
	if team == "" {
		return "", errors.New("authn: cloudflare access: empty team domain")
	}
	issuer := "https://" + team
	u, err := url.Parse(issuer)
	if err != nil || u.Host != team || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("authn: cloudflare access: team domain %q must be a bare host, like team.cloudflareaccess.com", team)
	}
	return issuer, nil
}
