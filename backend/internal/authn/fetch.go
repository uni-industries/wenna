package authn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// maxRedirects bounds redirects on calls to a provider.
const maxRedirects = 5

// httpsURL refuses anything but an absolute https URL with a host and no
// credentials or fragment: keys fetched over plain http could be swapped on
// the network path.
func httpsURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", raw, err)
	}
	switch {
	case u.Scheme != "https":
		return nil, fmt.Errorf("%q is not https", raw)
	case u.Host == "":
		return nil, fmt.Errorf("%q has no host", raw)
	case u.User != nil:
		return nil, fmt.Errorf("%q carries credentials", u.Redacted())
	case u.Fragment != "" || u.RawFragment != "":
		return nil, fmt.Errorf("%q has a fragment", raw)
	}
	return u, nil
}

// providerClient returns a copy of client that follows a redirect only to an
// https URL: a provider answering https must not be able to send a key fetch
// to plain http.
func providerClient(client *http.Client) *http.Client {
	c := *client
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("more than %d redirects", maxRedirects)
		}
		if _, err := httpsURL(req.URL.String()); err != nil {
			return fmt.Errorf("redirect refused: %w", err)
		}
		return nil
	}
	return &c
}

// getJSON GETs an https URL and decodes a JSON body of at most limit bytes
// into v. The caller bounds time through ctx.
func getJSON(ctx context.Context, client *http.Client, raw string, limit int64, v any) (err error) {
	if _, err := httpsURL(raw); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return fmt.Errorf("request %s: %w", raw, err)
	}
	req.Header.Set("Accept", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("get %s: %w", raw, err)
	}
	defer func() {
		if cerr := res.Body.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("close %s body: %w", raw, cerr))
		}
	}()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("get %s: status %d", raw, res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return fmt.Errorf("read %s: %w", raw, err)
	}
	if int64(len(body)) > limit {
		return fmt.Errorf("get %s: body exceeds %d bytes", raw, limit)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("decode %s: %w", raw, err)
	}
	return nil
}
