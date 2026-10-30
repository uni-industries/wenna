package authn

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// signingAlgs are the JWS algorithms accepted. Anything else, "none" and HS*
// included, is refused before a key is looked at.
var signingAlgs = []jose.SignatureAlgorithm{jose.RS256, jose.ES256}

const (
	// minRefresh bounds how often a token naming an unknown key id can make
	// the server fetch the provider's keys, whether the last attempt succeeded
	// or not. Without it every forged token costs a call to the provider
	// (go-oidc's RemoteKeySet refetches on every miss).
	minRefresh = time.Minute
	// maxKeyAge is how long keys are used before a background refresh, so a
	// key the provider revokes stops verifying within about this long.
	maxKeyAge = time.Hour
	// fetchTimeout bounds one refresh, discovery included. Refreshes run
	// detached from any request, so a caller hanging up cannot cancel one.
	fetchTimeout = 10 * time.Second
	// maxBodyBytes bounds every provider response read.
	maxBodyBytes = 1 << 20
)

var errNoKey = errors.New("no signing key matches the token")

// keySet is an oidc.KeySet over a provider's JWKS. Verification reads a
// snapshot and never waits on I/O unless the token names a key not in it.
// At most one fetch runs at a time, detached from the request that started
// it.
type keySet struct {
	// jwksURL returns where the keys are; it may itself fetch (discovery).
	jwksURL func(ctx context.Context, client *http.Client) (string, error)
	client  *http.Client
	now     func() time.Time
	timeout time.Duration // per fetch; fetchTimeout outside tests
	logger  *slog.Logger

	// mu guards the fields below. It is never held across I/O.
	mu        sync.Mutex
	keys      []jose.JSONWebKey
	fetchedAt time.Time     // last successful fetch
	attempted time.Time     // last fetch started, successful or not
	inflight  chan struct{} // non-nil while a fetch runs; closed when it ends
}

func newKeySet(jwksURL func(context.Context, *http.Client) (string, error), client *http.Client, now func() time.Time, logger *slog.Logger) *keySet {
	return &keySet{jwksURL: jwksURL, client: providerClient(client), now: now, timeout: fetchTimeout, logger: logger}
}

// VerifySignature implements oidc.KeySet.
func (k *keySet) VerifySignature(ctx context.Context, raw string) ([]byte, error) {
	jws, err := jose.ParseSigned(raw, signingAlgs)
	if err != nil {
		return nil, fmt.Errorf("parse token: %w", err)
	}
	if len(jws.Signatures) != 1 {
		return nil, errors.New("token must carry exactly one signature")
	}
	hdr := jws.Signatures[0].Header

	keys, aged := k.snapshot()
	if payload, ok := verifyWith(jws, hdr, keys); ok {
		if aged {
			k.start() // revalidate in the background; this token stands
		}
		return payload, nil
	}
	done := k.start()
	if done == nil {
		return nil, errNoKey // fetched recently; an unknown kid costs nothing
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-done:
	}
	keys, _ = k.snapshot()
	if payload, ok := verifyWith(jws, hdr, keys); ok {
		return payload, nil
	}
	return nil, errNoKey
}

// snapshot returns the cached keys and whether they are older than maxKeyAge.
func (k *keySet) snapshot() ([]jose.JSONWebKey, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.keys, k.now().Sub(k.fetchedAt) >= maxKeyAge
}

func verifyWith(jws *jose.JSONWebSignature, hdr jose.Header, keys []jose.JSONWebKey) ([]byte, bool) {
	for _, key := range keys {
		if hdr.KeyID != "" && key.KeyID != hdr.KeyID {
			continue
		}
		// A key published for one algorithm verifies only that algorithm.
		if key.Algorithm != "" && key.Algorithm != hdr.Algorithm {
			continue
		}
		if payload, err := jws.Verify(&key); err == nil {
			return payload, true
		}
	}
	return nil, false
}

// start begins a fetch unless one is running (returns its channel) or one
// started within minRefresh (returns nil).
func (k *keySet) start() <-chan struct{} {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.inflight != nil {
		return k.inflight
	}
	if !k.attempted.IsZero() && k.now().Sub(k.attempted) < minRefresh {
		return nil
	}
	k.attempted = k.now()
	done := make(chan struct{})
	k.inflight = done
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), k.timeout)
		defer cancel()
		keys, err := k.fetch(ctx)
		if err != nil {
			// Keys already cached stay in use; a revocation is missed until a
			// fetch succeeds, so this must be seen.
			k.logger.Error("refresh provider signing keys", "err", err)
		}

		k.mu.Lock()
		defer k.mu.Unlock()
		if err == nil {
			k.keys = keys
			k.fetchedAt = k.now()
		}
		k.inflight = nil
		close(done)
	}()
	return done
}

func (k *keySet) fetch(ctx context.Context) ([]jose.JSONWebKey, error) {
	u, err := k.jwksURL(ctx, k.client)
	if err != nil {
		return nil, fmt.Errorf("locate jwks: %w", err)
	}
	var set jose.JSONWebKeySet
	if err := getJSON(ctx, k.client, u, maxBodyBytes, &set); err != nil {
		return nil, fmt.Errorf("jwks: %w", err)
	}
	var keys []jose.JSONWebKey
	for _, key := range set.Keys {
		if key.Valid() && key.IsPublic() && (key.Use == "" || key.Use == "sig") {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("jwks at %s has no public signing keys", u)
	}
	return keys, nil
}
