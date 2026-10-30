// Package config is the one reader of the environment for the backend
// module, tests included: every variable, its default and its rule live in
// this file. Values are one validated struct, parsed once at start
// (docs/tooling/go.md § Conventions; CLAUDE.md § Environments differ by
// configuration, never by code).
//
// Defaults are local values that fail closed: a database in the user's home,
// a loopback listener. The token never defaults. Set-but-empty is always an
// error.
//
//	Variable               Used by   Default               Rule
//	WENNA_DB                all       $HOME/.wenna/wenna.db   absolute path; if set, its directory must exist
//	WENNA_TOKEN             serve     —                     at least 32 bytes, trimmed
//	PORT                   serve     8080                  1-65535
//	WENNA_LISTEN_HOST       serve     127.0.0.1             IP literal; loopback unless the next is true
//	WENNA_BEHIND_TLS_PROXY  serve     false                 true or false
//	WENNA_TRIGRAM           all       true                  true or false
//	WENNA_PUBLIC_URL        serve     —                     canonical https origin; the one public host accepted on loopback; must be WENNA_WORKOS_RESOURCE's origin
//
// Two OAuth providers for `wenna serve`, each off unless its whole group is
// set; part of a group is an error naming what is missing. None is a secret.
//
//	Variable                 Rule
//	WENNA_ACCESS_TEAM_DOMAIN  DNS hostname, at least two labels: no scheme, port, path or trailing dot; lowercased
//	WENNA_ACCESS_AUD          no whitespace or control characters
//	WENNA_ACCESS_OWNER_EMAIL  trimmed; one "@" with text either side; no whitespace or control characters
//	WENNA_WORKOS_ISSUER       canonical https URL of a host: no path, trailing slash, query, fragment or userinfo
//	WENNA_WORKOS_RESOURCE     canonical https URL with a path: no trailing slash, query, fragment or userinfo
//
// Canonical: lower-case scheme and host, no :443, port without leading zeros.
//
//	WENNA_WORKOS_OWNER_ID     no whitespace or control characters
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// Variable names.
const (
	EnvDB         = "WENNA_DB"
	EnvToken      = "WENNA_TOKEN"
	EnvPort       = "PORT"
	EnvListenHost = "WENNA_LISTEN_HOST"
	// EnvBehindTLSProxy asserts that every request reaches the listener
	// through a proxy that terminated TLS, the property that makes a wider
	// address safe. wenna cannot check it: proxies do not reliably say so.
	EnvBehindTLSProxy = "WENNA_BEHIND_TLS_PROXY"
	// EnvTrigram turns on the trigram index, which lets recall match text
	// inside a word at the cost of a larger database and slower writes. On by
	// default: recall accuracy outweighs the disk for one person's memory.
	EnvTrigram = "WENNA_TRIGRAM"
	// EnvPublicURL is the https origin clients call when a proxy forwards
	// to loopback with the public Host header (Tailscale Funnel); requests
	// arriving on loopback are otherwise refused unless Host is localhost.
	EnvPublicURL = "WENNA_PUBLIC_URL"

	// Cloudflare Access managed OAuth (Access).
	EnvAccessTeamDomain = "WENNA_ACCESS_TEAM_DOMAIN"
	EnvAccessAudience   = "WENNA_ACCESS_AUD"
	EnvAccessOwnerEmail = "WENNA_ACCESS_OWNER_EMAIL"
	// WorkOS AuthKit (WorkOS).
	EnvWorkOSIssuer   = "WENNA_WORKOS_ISSUER"
	EnvWorkOSResource = "WENNA_WORKOS_RESOURCE"
	EnvWorkOSOwnerID  = "WENNA_WORKOS_OWNER_ID"

	// envHome locates the default database.
	envHome = "HOME"
)

// Defaults apply only when a variable is unset. DefaultListenHost is
// reachable from this machine only, and so is every value allowed.
const (
	DefaultListenHost = "127.0.0.1"
	DefaultPort       = "8080"
	// DefaultDB is the database path under $HOME.
	DefaultDB = ".wenna/wenna.db"
)

// MinTokenBytes refuses a guessable token; `openssl rand -hex 32` makes 64.
const MinTokenBytes = 32

var (
	errUnset     = errors.New("is required and unset")
	errEmpty     = errors.New("is set but empty")
	errPortDigit = errors.New("is not a decimal port number")
	errPortRange = errors.New("is out of range 1-65535")
	errHostIP    = errors.New("is not an IP address")
	errBool      = errors.New(`is neither "true" nor "false"`)
	// errNotLoopback is the refusal an operator most needs to understand, so
	// it says why and what to do instead.
	errNotLoopback = errors.New("is not a loopback address. REFUSED: wenna serve speaks plain HTTP, " +
		"so the token and every memory would cross the network unencrypted, readable and replayable by anyone on it. " +
		"It listens on this machine only (127.0.0.1 or ::1). To reach it from elsewhere, run a TLS proxy " +
		"or an SSH tunnel on this machine that forwards to the loopback address. To self-host behind a " +
		"platform that serves HTTPS, see HOSTING.md")
	errShort    = fmt.Errorf("is shorter than %d bytes", MinTokenBytes)
	errRelative = errors.New("is not an absolute path")
	errNoHome   = fmt.Errorf("is unset and so is %s, which locates the default", envHome)

	errPartial    = errors.New("unset; set all or none of")
	errHostname   = errors.New("is not a DNS hostname with at least two labels (no scheme, port, path or trailing dot)")
	errSpace      = errors.New("contains whitespace or a control character")
	errEmail      = errors.New(`is not an email address: one "@" with text either side`)
	errHTTPSURL   = errors.New("is not an https URL without query, fragment or userinfo")
	errHasPath    = errors.New("has a path or trailing slash; give the scheme and host only")
	errNoPath     = errors.New("has no path; give the full URL clients call")
	errTrailSlash = errors.New("ends in a slash; remove it")
	// errNotCanonical: token claims are compared byte for byte, so a URL
	// written another way would never match.
	errNotCanonical = errors.New("is not in canonical form (lower-case scheme and host, no :443)")
	errOrigin       = errors.New("is not at the origin of " + EnvPublicURL + ": wenna has one public origin")
)

// Config is what every command takes from its environment.
type Config struct {
	// DBPath is the SQLite database file.
	DBPath string
	// DBDirMustExist is true when WENNA_DB is set: its directory is then a
	// mount or a place the operator chose, and creating it would hide a
	// missing volume behind a disk that is thrown away. The default's
	// directory is created.
	DBDirMustExist bool
	// Trigram turns on the store's trigram index (sqlitestore.WithTrigram).
	Trigram bool
}

// Serve is what `wenna serve` takes from its environment.
type Serve struct {
	Config
	// Addr is the TCP listen address, "<WENNA_LISTEN_HOST>:<PORT>". Port 0 is
	// refused: an OS-chosen port is unreachable by whatever is configured to
	// call it.
	Addr string
	// Token is the bearer token every HTTP request must carry.
	Token Secret
	// Access is Cloudflare Access managed OAuth; nil unless configured.
	Access *Access
	// WorkOS is WorkOS AuthKit; nil unless configured.
	WorkOS *WorkOS
	// PublicURL is the https origin clients call, canonical, no path; empty
	// unless configured.
	PublicURL string
	// PublicHost is PublicURL's host name, without port or IPv6 brackets;
	// empty when PublicURL is.
	PublicHost string
}

// Access configures Cloudflare Access managed OAuth.
type Access struct {
	// TeamDomain is the team's hostname, lowercased, e.g.
	// acme.cloudflareaccess.com.
	TeamDomain string
	// Audience is the Access application's AUD tag.
	Audience string
	// OwnerEmail is the one user's email as Access asserts it.
	OwnerEmail string
}

// WorkOS configures WorkOS AuthKit.
type WorkOS struct {
	// Issuer is the AuthKit URL, scheme and host only, as configured.
	Issuer string
	// Resource is wenna's public MCP URL, as configured.
	Resource string
	// OwnerSubject is the one user's WorkOS user ID.
	OwnerSubject string
}

// Load parses the configuration every command needs. lookup has
// os.LookupEnv's contract; main passes os.LookupEnv, tests pass a map, so
// tests stay parallel (no t.Setenv).
func Load(lookup func(string) (string, bool)) (Config, error) {
	db, explicit, err := loadDB(lookup)
	if err != nil {
		return Config{}, err
	}
	trigram, err := loadBool(lookup, EnvTrigram, true)
	if err != nil {
		return Config{}, err
	}
	return Config{DBPath: db, DBDirMustExist: explicit, Trigram: trigram}, nil
}

// LoadServe parses the configuration of the HTTP server.
func LoadServe(lookup func(string) (string, bool)) (Serve, error) {
	c, err := Load(lookup)
	if err != nil {
		return Serve{}, err
	}
	behindProxy, err := loadBool(lookup, EnvBehindTLSProxy, false)
	if err != nil {
		return Serve{}, err
	}
	host, err := loadListenHost(lookup, behindProxy)
	if err != nil {
		return Serve{}, err
	}
	port, err := loadPort(lookup)
	if err != nil {
		return Serve{}, err
	}
	tok, err := loadToken(lookup)
	if err != nil {
		return Serve{}, err
	}
	access, err := loadAccess(lookup)
	if err != nil {
		return Serve{}, err
	}
	workos, err := loadWorkOS(lookup)
	if err != nil {
		return Serve{}, err
	}
	public, publicHost, err := loadPublicURL(lookup, workos)
	if err != nil {
		return Serve{}, err
	}
	return Serve{Config: c, Addr: net.JoinHostPort(host, port), Token: tok, Access: access, WorkOS: workos, PublicURL: public, PublicHost: publicHost}, nil
}

// loadPublicURL takes a canonical https origin and its host name, or "" for
// both when unset. With WorkOS on, its resource must be at that origin: the
// trusted origin is one value.
func loadPublicURL(lookup func(string) (string, bool), workos *WorkOS) (public, host string, err error) {
	v, ok := lookup(EnvPublicURL)
	if !ok {
		return "", "", nil
	}
	if v == "" {
		return "", "", fmt.Errorf("%s %w", EnvPublicURL, errEmpty)
	}
	pu, err := parseHTTPSURL(EnvPublicURL, v, false)
	if err != nil {
		return "", "", err
	}
	if workos == nil {
		return v, pu.Hostname(), nil
	}
	// Both are canonical, so their origins compare byte for byte.
	u, err := url.Parse(workos.Resource)
	if err != nil {
		return "", "", fmt.Errorf("%s=%q %w", EnvWorkOSResource, workos.Resource, errHTTPSURL)
	}
	if u.Scheme+"://"+u.Host != v {
		return "", "", fmt.Errorf("%s=%q %w (%q)", EnvWorkOSResource, workos.Resource, errOrigin, v)
	}
	return v, pu.Hostname(), nil
}

// loadGroup reads names, which configure one optional feature together. It
// returns nil when none is set, the values in order when all are, and an
// error naming the empty or missing ones otherwise.
func loadGroup(lookup func(string) (string, bool), names ...string) ([]string, error) {
	vals := make([]string, len(names))
	var missing []string
	for i, n := range names {
		v, ok := lookup(n)
		switch {
		case !ok:
			missing = append(missing, n)
		case v == "":
			return nil, fmt.Errorf("%s %w", n, errEmpty)
		}
		vals[i] = v
	}
	switch len(missing) {
	case len(names):
		return nil, nil
	case 0:
		return vals, nil
	}
	return nil, fmt.Errorf("%s %w: %s", strings.Join(missing, ", "), errPartial, strings.Join(names, ", "))
}

func loadAccess(lookup func(string) (string, bool)) (*Access, error) {
	v, err := loadGroup(lookup, EnvAccessTeamDomain, EnvAccessAudience, EnvAccessOwnerEmail)
	if v == nil || err != nil {
		return nil, err
	}
	domain, err := parseHostname(EnvAccessTeamDomain, v[0])
	if err != nil {
		return nil, err
	}
	if err := checkNoSpace(EnvAccessAudience, v[1]); err != nil {
		return nil, err
	}
	email, err := parseEmail(EnvAccessOwnerEmail, v[2])
	if err != nil {
		return nil, err
	}
	return &Access{TeamDomain: domain, Audience: v[1], OwnerEmail: email}, nil
}

func loadWorkOS(lookup func(string) (string, bool)) (*WorkOS, error) {
	v, err := loadGroup(lookup, EnvWorkOSIssuer, EnvWorkOSResource, EnvWorkOSOwnerID)
	if v == nil || err != nil {
		return nil, err
	}
	if _, err := parseHTTPSURL(EnvWorkOSIssuer, v[0], false); err != nil {
		return nil, err
	}
	if _, err := parseHTTPSURL(EnvWorkOSResource, v[1], true); err != nil {
		return nil, err
	}
	if err := checkNoSpace(EnvWorkOSOwnerID, v[2]); err != nil {
		return nil, err
	}
	return &WorkOS{Issuer: v[0], Resource: v[1], OwnerSubject: v[2]}, nil
}

// parseHostname takes a fully qualified DNS name and returns it lowercased:
// letters, digits and hyphens in dot-separated labels of 1-63 bytes, no label
// starting or ending with a hyphen, at least two labels, no IP literal.
// Anything a URL would add (scheme, port, path) fails the character rule.
func parseHostname(name, v string) (string, error) {
	h := strings.ToLower(v)
	bad := fmt.Errorf("%s=%q %w", name, v, errHostname)
	labels := strings.Split(h, ".")
	if len(h) > 253 || len(labels) < 2 || net.ParseIP(h) != nil {
		return "", bad
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return "", bad
		}
		if strings.Trim(l, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
			return "", bad
		}
	}
	return h, nil
}

// checkNoSpace refuses whitespace and control characters: an identifier
// compared against a token claim never contains them, so one present is a
// paste error. The value is personal or an identifier, so it is not echoed.
func checkNoSpace(name, v string) error {
	if strings.IndexFunc(v, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return fmt.Errorf("%s %w", name, errSpace)
	}
	return nil
}

// parseEmail trims surrounding whitespace, then takes one "@" with text on
// either side. The value is personal data, so errors do not echo it.
func parseEmail(name, v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", fmt.Errorf("%s %w", name, errEmpty)
	}
	if err := checkNoSpace(name, v); err != nil {
		return "", err
	}
	// Without an "@", Cut returns all of v as local and an empty domain.
	local, domain, _ := strings.Cut(v, "@")
	if local == "" || domain == "" || strings.Contains(domain, "@") {
		return "", fmt.Errorf("%s %w", name, errEmail)
	}
	return v, nil
}

// parseHTTPSURL takes an https URL with a host and no userinfo, query or
// fragment. wantPath requires a path without a trailing slash; otherwise any
// path, "/" included, is refused. Issuers and audiences are compared byte for
// byte against token claims, so the value must already be canonical (lower
// case scheme and host, no default port) and is never rewritten: a
// non-canonical one is refused, naming the canonical form. It returns v
// parsed.
func parseHTTPSURL(name, v string, wantPath bool) (*url.URL, error) {
	u, err := url.Parse(v)
	// An opaque URL ("https:host") has no host; any "#" starts a fragment,
	// empty ones included, which u.Fragment cannot show. url.Parse lowercases
	// the scheme, so "HTTPS://" passes here and fails the canonical check.
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || strings.Contains(v, "#") {
		return nil, fmt.Errorf("%s=%q %w", name, v, errHTTPSURL)
	}
	switch {
	case !wantPath && (u.Path != "" || u.RawPath != ""):
		return nil, fmt.Errorf("%s=%q %w", name, v, errHasPath)
	case wantPath && (u.Path == "" || u.Path == "/"):
		return nil, fmt.Errorf("%s=%q %w", name, v, errNoPath)
	case wantPath && strings.HasSuffix(u.Path, "/"):
		return nil, fmt.Errorf("%s=%q %w", name, v, errTrailSlash)
	}
	canon, ok := canonicalHTTPS(v, u)
	if !ok {
		return nil, fmt.Errorf("%s=%q %w", name, v, errHTTPSURL)
	}
	if canon != v {
		return nil, fmt.Errorf("%s=%q %w: use %q", name, v, errNotCanonical, canon)
	}
	return u, nil
}

// canonicalHTTPS returns v, already checked by parseHTTPSURL, with the
// scheme and host lowercased and the port written without leading zeros, or
// dropped when it is 443. ok is false for port 0.
func canonicalHTTPS(v string, u *url.URL) (string, bool) {
	host := strings.ToLower(u.Hostname())
	hostport := host
	if port := u.Port(); port != "" {
		// url.Parse has checked the port is digits only.
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return "", false
		}
		if n != 443 {
			hostport = net.JoinHostPort(host, strconv.FormatUint(n, 10))
		}
	}
	if hostport == host && strings.Contains(host, ":") {
		hostport = "[" + host + "]"
	}
	// The path is everything after the authority, as written: no userinfo,
	// query or fragment remain, so the authority ends at the first "/".
	rest := v[len("https://"):]
	path := ""
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		path = rest[i:]
	}
	return "https://" + hostport + path, true
}

// loadDB takes an absolute path only: AI apps start wenna from directories of
// their choosing, so a relative path would name a different file per app.
// explicit reports whether the variable was set.
func loadDB(lookup func(string) (string, bool)) (path string, explicit bool, err error) {
	v, ok := lookup(EnvDB)
	if !ok {
		home, ok := lookup(envHome)
		if !ok || home == "" {
			return "", false, fmt.Errorf("%s %w", EnvDB, errNoHome)
		}
		v = filepath.Join(home, DefaultDB)
	}
	if v == "" {
		return "", false, fmt.Errorf("%s %w", EnvDB, errEmpty)
	}
	if !filepath.IsAbs(v) {
		return "", false, fmt.Errorf("%s=%q %w", EnvDB, v, errRelative)
	}
	return filepath.Clean(v), ok, nil
}

// loadToken never puts the value into the returned error. Surrounding
// whitespace is trimmed before any check (a value pasted with a trailing
// newline is common); bearer trims the header the same way.
func loadToken(lookup func(string) (string, bool)) (Secret, error) {
	v, ok := lookup(EnvToken)
	if !ok {
		return Secret{}, fmt.Errorf("%s %w", EnvToken, errUnset)
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return Secret{}, fmt.Errorf("%s %w", EnvToken, errEmpty)
	}
	if len(v) < MinTokenBytes {
		return Secret{}, fmt.Errorf("%s %w", EnvToken, errShort)
	}
	return Secret{v: v}, nil
}

// loadListenHost takes an IP literal only. A hostname would resolve at bind
// time, so what it binds could change without the value changing. Loopback
// only unless behindProxy: the server has no TLS, so a wider address with
// nothing terminating TLS in front sends the bearer token and the user's
// memories in the clear.
func loadListenHost(lookup func(string) (string, bool), behindProxy bool) (string, error) {
	v, ok := lookup(EnvListenHost)
	if !ok {
		v = DefaultListenHost
	}
	if v == "" {
		return "", fmt.Errorf("%s %w", EnvListenHost, errEmpty)
	}
	ip := net.ParseIP(v)
	if ip == nil {
		return "", fmt.Errorf("%s=%q %w", EnvListenHost, v, errHostIP)
	}
	if !ip.IsLoopback() && !behindProxy {
		return "", fmt.Errorf("%s=%q %w", EnvListenHost, v, errNotLoopback)
	}
	return ip.String(), nil
}

// loadBool takes exactly "true" or "false"; unset is false. Anything else is
// refused rather than guessed, so a typo cannot flip a setting.
func loadBool(lookup func(string) (string, bool), name string, def bool) (bool, error) {
	v, ok := lookup(name)
	switch {
	case !ok:
		return def, nil
	case v == "":
		return false, fmt.Errorf("%s %w", name, errEmpty)
	case v == "true":
		return true, nil
	case v == "false":
		return false, nil
	}
	return false, fmt.Errorf("%s=%q %w", name, v, errBool)
}

func loadPort(lookup func(string) (string, bool)) (string, error) {
	v, ok := lookup(EnvPort)
	if !ok {
		v = DefaultPort
	}
	if v == "" {
		return "", fmt.Errorf("%s %w", EnvPort, errEmpty)
	}
	if strings.Trim(v, "0123456789") != "" {
		return "", fmt.Errorf("%s=%q %w", EnvPort, v, errPortDigit)
	}
	// Digits only, so the one failure left is a value above 16 bits.
	n, err := strconv.ParseUint(v, 10, 16)
	if err != nil {
		return "", fmt.Errorf("%s=%q %w: %w", EnvPort, v, errPortRange, err)
	}
	if n == 0 {
		return "", fmt.Errorf("%s=%q %w", EnvPort, v, errPortRange)
	}
	return strconv.FormatUint(n, 10), nil
}
