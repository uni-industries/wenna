package authn

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// WorkOS configures the WorkOS AuthKit verifier. Issuer is the AuthKit
// domain as an https URL; Resource is wenna's public MCP URL, which tokens
// must name as their audience; OwnerSubject is the owner's user id, compared
// exactly.
type WorkOS struct{ Issuer, Resource, OwnerSubject string }

// NewWorkOS returns a Verifier for WorkOS AuthKit access tokens. It does no
// network I/O: the issuer's metadata (RFC 8414) is read on the first key
// fetch, and refused then if it names another issuer or a non-https jwks_uri.
func NewWorkOS(c WorkOS, client *http.Client, now func() time.Time, logger *slog.Logger) (*Verifier, error) {
	if err := check(client, now, logger); err != nil {
		return nil, err
	}
	meta, err := metadataURL(c.Issuer)
	if err != nil {
		return nil, fmt.Errorf("authn: workos issuer: %w", err)
	}
	if strings.TrimSpace(c.Resource) == "" {
		return nil, errors.New("authn: workos: empty resource")
	}
	if strings.TrimSpace(c.OwnerSubject) == "" {
		return nil, errors.New("authn: workos: empty owner subject")
	}
	issuer, owner := c.Issuer, c.OwnerSubject
	keys := newKeySet(func(ctx context.Context, client *http.Client) (string, error) {
		return discoverJWKS(ctx, client, issuer, meta)
	}, client, now, logger)
	return newVerifier("workos", issuer, c.Resource, keys, now, func(tok *oidc.IDToken) error {
		if tok.Subject != owner {
			return errNotOwner
		}
		return nil
	}), nil
}

// metadataURL returns the RFC 8414 metadata URL for issuer: the well-known
// segment goes between the host and any path.
func metadataURL(issuer string) (string, error) {
	u, err := httpsURL(issuer)
	if err != nil {
		return "", err
	}
	if u.RawQuery != "" || u.ForceQuery {
		return "", fmt.Errorf("%q has a query", issuer)
	}
	return "https://" + u.Host + "/.well-known/oauth-authorization-server" + strings.TrimSuffix(u.EscapedPath(), "/"), nil
}

// discoverJWKS reads the issuer's metadata and returns its jwks_uri.
func discoverJWKS(ctx context.Context, client *http.Client, issuer, meta string) (string, error) {
	var m struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	if err := getJSON(ctx, client, meta, maxBodyBytes, &m); err != nil {
		return "", fmt.Errorf("issuer metadata: %w", err)
	}
	// Token "iss" claims are compared exactly, so a near-miss here would
	// refuse every token; say so instead.
	if m.Issuer != issuer {
		return "", fmt.Errorf("issuer metadata names %q, configured %q: they must match exactly", m.Issuer, issuer)
	}
	if m.JWKSURI == "" {
		return "", errors.New("issuer metadata has no jwks_uri")
	}
	return m.JWKSURI, nil // getJSON refuses it unless https
}
