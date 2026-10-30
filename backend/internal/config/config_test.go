package config

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
)

const (
	validToken = "tok-0123456789abcdef0123456789abcdef"
	home       = "/home/u"
	defaultDB  = "/home/u/.wenna/wenna.db"
)

// unset, as an override value, removes the variable.
const unset = "\x00unset"

func lookupFrom(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	}
}

// env is HOME and a valid token plus overrides, so each case tests one value.
func env(overrides map[string]string) map[string]string {
	out := map[string]string{"HOME": home, EnvToken: validToken}
	maps.Copy(out, overrides)
	for k, v := range out {
		if v == unset {
			delete(out, k)
		}
	}
	return out
}

func serve(addr, db string) Serve {
	return Serve{Config: Config{DBPath: db, Trigram: true}, Addr: addr, Token: Secret{v: validToken}}
}

// TestHostInjectedNames pins names other software sets, written out rather
// than read from the constants, which every other test uses and so would
// follow a rename silently. HOME: the shell. PORT: hosts that assign one.
func TestHostInjectedNames(t *testing.T) {
	t.Parallel()
	got, err := LoadServe(lookupFrom(map[string]string{
		"HOME": home, "PORT": "9000", EnvToken: validToken,
		// Written into the deploy files at the repo root.
		"WENNA_LISTEN_HOST": "0.0.0.0", "WENNA_BEHIND_TLS_PROXY": "true",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Addr != "0.0.0.0:9000" || got.DBPath != defaultDB {
		t.Fatalf("LoadServe() = %#v", got)
	}
}

func TestLoad(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		env     map[string]string
		want    string
		wantErr error
	}{
		{name: "default under home", env: env(nil), want: defaultDB},
		{name: "set", env: env(map[string]string{EnvDB: "/data/m.db"}), want: "/data/m.db"},
		{name: "cleaned", env: env(map[string]string{EnvDB: "/data/../data//m.db"}), want: "/data/m.db"},
		{name: "set without home", env: env(map[string]string{"HOME": unset, EnvDB: "/data/m.db"}), want: "/data/m.db"},
		{name: "no token needed", env: env(map[string]string{EnvToken: unset}), want: defaultDB},
		{name: "empty", env: env(map[string]string{EnvDB: ""}), wantErr: errEmpty},
		{name: "relative", env: env(map[string]string{EnvDB: "wenna.db"}), wantErr: errRelative},
		{name: "home unset", env: env(map[string]string{"HOME": unset}), wantErr: errNoHome},
		{name: "home empty", env: env(map[string]string{"HOME": ""}), wantErr: errNoHome},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := Load(lookupFrom(tc.env))
			if tc.wantErr == nil {
				if err != nil || got.DBPath != tc.want {
					t.Fatalf("Load() = %q, %v; want %q", got.DBPath, err, tc.want)
				}
				// Only a path the operator set must already have its directory.
				if _, set := tc.env[EnvDB]; got.DBDirMustExist != set {
					t.Fatalf("Load() DBDirMustExist = %v, want %v", got.DBDirMustExist, set)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) || !strings.Contains(err.Error(), EnvDB) {
				t.Fatalf("Load() error = %v, want %v naming %s", err, tc.wantErr, EnvDB)
			}
		})
	}
}

func TestLoadServe(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		env     map[string]string
		want    Serve
		wantErr error
		wantVar string
	}{
		{name: "defaults", env: env(nil), want: serve("127.0.0.1:8080", defaultDB)},
		{name: "port", env: env(map[string]string{EnvPort: "9090"}), want: serve("127.0.0.1:9090", defaultDB)},
		{name: "lowest port", env: env(map[string]string{EnvPort: "1"}), want: serve("127.0.0.1:1", defaultDB)},
		{name: "highest port", env: env(map[string]string{EnvPort: "65535"}), want: serve("127.0.0.1:65535", defaultDB)},
		{name: "leading zeros normalised", env: env(map[string]string{EnvPort: "08080"}), want: serve("127.0.0.1:8080", defaultDB)},
		{name: "loopback v6", env: env(map[string]string{EnvListenHost: "::1", EnvPort: "1"}), want: serve("[::1]:1", defaultDB)},
		{name: "other loopback v4", env: env(map[string]string{EnvListenHost: "127.0.0.2"}), want: serve("127.0.0.2:8080", defaultDB)},
		{name: "v4-mapped loopback", env: env(map[string]string{EnvListenHost: "::ffff:127.0.0.1"}), want: serve("127.0.0.1:8080", defaultDB)},

		{name: "port empty", env: env(map[string]string{EnvPort: ""}), wantErr: errEmpty, wantVar: EnvPort},
		{name: "host and port", env: env(map[string]string{EnvPort: "127.0.0.1:8080"}), wantErr: errPortDigit, wantVar: EnvPort},
		{name: "named port", env: env(map[string]string{EnvPort: "http"}), wantErr: errPortDigit, wantVar: EnvPort},
		{name: "signed port", env: env(map[string]string{EnvPort: "+80"}), wantErr: errPortDigit, wantVar: EnvPort},
		{name: "port with space", env: env(map[string]string{EnvPort: " 80"}), wantErr: errPortDigit, wantVar: EnvPort},
		{name: "port zero", env: env(map[string]string{EnvPort: "0"}), wantErr: errPortRange, wantVar: EnvPort},
		{name: "port above range", env: env(map[string]string{EnvPort: "65536"}), wantErr: errPortRange, wantVar: EnvPort},
		{name: "port overflows int", env: env(map[string]string{EnvPort: "99999999999999999999"}), wantErr: errPortRange, wantVar: EnvPort},

		{name: "host empty", env: env(map[string]string{EnvListenHost: ""}), wantErr: errEmpty, wantVar: EnvListenHost},
		{name: "host name", env: env(map[string]string{EnvListenHost: "localhost"}), wantErr: errHostIP, wantVar: EnvListenHost},
		{name: "host with port", env: env(map[string]string{EnvListenHost: "127.0.0.1:80"}), wantErr: errHostIP, wantVar: EnvListenHost},
		{name: "host bracketed", env: env(map[string]string{EnvListenHost: "[::1]"}), wantErr: errHostIP, wantVar: EnvListenHost},
		// Plain HTTP carries the token: nothing beyond loopback.
		{name: "all interfaces v4", env: env(map[string]string{EnvListenHost: "0.0.0.0"}), wantErr: errNotLoopback, wantVar: EnvListenHost},
		{name: "all interfaces v6", env: env(map[string]string{EnvListenHost: "::"}), wantErr: errNotLoopback, wantVar: EnvListenHost},
		{name: "private address", env: env(map[string]string{EnvListenHost: "192.168.1.5"}), wantErr: errNotLoopback, wantVar: EnvListenHost},
		{name: "public address", env: env(map[string]string{EnvListenHost: "203.0.113.7"}), wantErr: errNotLoopback, wantVar: EnvListenHost},
		{name: "link-local v6", env: env(map[string]string{EnvListenHost: "fe80::1"}), wantErr: errNotLoopback, wantVar: EnvListenHost},

		// Behind a TLS-terminating proxy, any IP; the refusal names the way out.
		{name: "proxy all interfaces v4", env: env(map[string]string{EnvBehindTLSProxy: "true", EnvListenHost: "0.0.0.0"}), want: serve("0.0.0.0:8080", defaultDB)},
		{name: "proxy all interfaces v6", env: env(map[string]string{EnvBehindTLSProxy: "true", EnvListenHost: "::"}), want: serve("[::]:8080", defaultDB)},
		{name: "proxy keeps loopback default", env: env(map[string]string{EnvBehindTLSProxy: "true"}), want: serve("127.0.0.1:8080", defaultDB)},
		{name: "proxy false", env: env(map[string]string{EnvBehindTLSProxy: "false", EnvListenHost: "0.0.0.0"}), wantErr: errNotLoopback, wantVar: EnvListenHost},
		{name: "proxy still needs IP", env: env(map[string]string{EnvBehindTLSProxy: "true", EnvListenHost: "example.com"}), wantErr: errHostIP, wantVar: EnvListenHost},
		{name: "proxy empty", env: env(map[string]string{EnvBehindTLSProxy: ""}), wantErr: errEmpty, wantVar: EnvBehindTLSProxy},
		{name: "proxy capitalised", env: env(map[string]string{EnvBehindTLSProxy: "True", EnvListenHost: "0.0.0.0"}), wantErr: errBool, wantVar: EnvBehindTLSProxy},
		{name: "proxy one", env: env(map[string]string{EnvBehindTLSProxy: "1"}), wantErr: errBool, wantVar: EnvBehindTLSProxy},
		{name: "proxy padded", env: env(map[string]string{EnvBehindTLSProxy: "true "}), wantErr: errBool, wantVar: EnvBehindTLSProxy},

		{name: "token unset", env: env(map[string]string{EnvToken: unset}), wantErr: errUnset, wantVar: EnvToken},
		{name: "token empty", env: env(map[string]string{EnvToken: ""}), wantErr: errEmpty, wantVar: EnvToken},
		{name: "token short", env: env(map[string]string{EnvToken: "secretpw-short-but-not-32-bytes"}), wantErr: errShort, wantVar: EnvToken},
		{name: "token whitespace trimmed", env: env(map[string]string{EnvToken: " \t" + validToken + " \n"}), want: serve("127.0.0.1:8080", defaultDB)},
		{name: "token trailing newline", env: env(map[string]string{EnvToken: validToken + "\n"}), want: serve("127.0.0.1:8080", defaultDB)},
		{name: "token only whitespace", env: env(map[string]string{EnvToken: " \t\r\n "}), wantErr: errEmpty, wantVar: EnvToken},
		// Padding must not count toward the minimum length.
		{name: "token short padded", env: env(map[string]string{EnvToken: "  secretpw-short-but-not-32-bytes\n"}), wantErr: errShort, wantVar: EnvToken},
		{name: "db error first", env: env(map[string]string{EnvDB: "rel"}), wantErr: errRelative, wantVar: EnvDB},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := LoadServe(lookupFrom(tc.env))
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("LoadServe() error = %v, want nil", err)
				}
				if got != tc.want {
					t.Fatalf("LoadServe() = %#v, want %#v", got, tc.want)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("LoadServe() error = %v, want errors.Is %v", err, tc.wantErr)
			}
			// The operator reads this line at start; it must name the variable.
			if !strings.Contains(err.Error(), tc.wantVar) {
				t.Errorf("LoadServe() error %q does not name %s", err, tc.wantVar)
			}
			if strings.Contains(err.Error(), "secretpw") {
				t.Errorf("LoadServe() error %q leaks the token", err)
			}
			if got != (Serve{}) {
				t.Errorf("LoadServe() = %#v on error, want zero", got)
			}
		})
	}
}

// TestLoadTrigram: WENNA_TRIGRAM is true or false, true when unset, for
// every command; anything else is refused, naming the variable.
func TestLoadTrigram(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		value   string
		want    bool
		wantErr error
	}{
		{name: "unset", value: unset, want: true},
		{name: "true", value: "true", want: true},
		{name: "false", value: "false", want: false},
		{name: "empty", value: "", wantErr: errEmpty},
		{name: "capitalised", value: "True", wantErr: errBool},
		{name: "one", value: "1", wantErr: errBool},
		{name: "yes", value: "yes", wantErr: errBool},
		{name: "padded", value: " true", wantErr: errBool},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := env(map[string]string{EnvTrigram: tc.value})
			got, err := Load(lookupFrom(e))
			gotServe, serveErr := LoadServe(lookupFrom(e))
			if tc.wantErr == nil {
				if err != nil || got.Trigram != tc.want {
					t.Fatalf("Load() Trigram = %v, %v; want %v", got.Trigram, err, tc.want)
				}
				if serveErr != nil || gotServe.Trigram != tc.want {
					t.Fatalf("LoadServe() Trigram = %v, %v; want %v", gotServe.Trigram, serveErr, tc.want)
				}
				return
			}
			for _, err := range []error{err, serveErr} {
				if !errors.Is(err, tc.wantErr) || !strings.Contains(err.Error(), EnvTrigram) {
					t.Errorf("error = %v, want %v naming %s", err, tc.wantErr, EnvTrigram)
				}
			}
			if got != (Config{}) || gotServe != (Serve{}) {
				t.Errorf("Load() = %#v, LoadServe() = %#v on error, want zero", got, gotServe)
			}
		})
	}
	// The name is written out: deploy files and docs spell it.
	if EnvTrigram != "WENNA_TRIGRAM" {
		t.Errorf("EnvTrigram = %q", EnvTrigram)
	}
}

const (
	teamDomain = "acme.cloudflareaccess.com"
	accessAUD  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	ownerEmail = "owner@example.com"
	issuer     = "https://acme-42.authkit.app"
	resource   = "https://memory.example.com/api/mcp"
	ownerID    = "user_01HXYZABCDEFGHJKMNPQRSTVWX"
)

func accessEnv(overrides map[string]string) map[string]string {
	m := map[string]string{EnvAccessTeamDomain: teamDomain, EnvAccessAudience: accessAUD, EnvAccessOwnerEmail: ownerEmail}
	maps.Copy(m, overrides)
	return env(m)
}

func workosEnv(overrides map[string]string) map[string]string {
	m := map[string]string{EnvWorkOSIssuer: issuer, EnvWorkOSResource: resource, EnvWorkOSOwnerID: ownerID}
	maps.Copy(m, overrides)
	return env(m)
}

// TestLoadServeOAuth: each provider is off unless its whole group is set;
// part of a group, or a value breaking its rule, is refused naming the
// variable.
func TestLoadServeOAuth(t *testing.T) {
	t.Parallel()
	wantAccess := &Access{TeamDomain: teamDomain, Audience: accessAUD, OwnerEmail: ownerEmail}
	wantWorkOS := &WorkOS{Issuer: issuer, Resource: resource, OwnerSubject: ownerID}
	both := accessEnv(nil)
	maps.Copy(both, workosEnv(nil))
	tests := []struct {
		name       string
		env        map[string]string
		wantAccess *Access
		wantWorkOS *WorkOS
		wantErr    error
		wantVar    string
		// wantIn must appear in the error: the canonical form to use.
		wantIn string
	}{
		{name: "neither", env: env(nil)},
		{name: "access", env: accessEnv(nil), wantAccess: wantAccess},
		{name: "workos", env: workosEnv(nil), wantWorkOS: wantWorkOS},
		{name: "both", env: both, wantAccess: wantAccess, wantWorkOS: wantWorkOS},

		// Access: partial.
		{name: "access no team domain", env: accessEnv(map[string]string{EnvAccessTeamDomain: unset}), wantErr: errPartial, wantVar: EnvAccessTeamDomain},
		{name: "access no aud", env: accessEnv(map[string]string{EnvAccessAudience: unset}), wantErr: errPartial, wantVar: EnvAccessAudience},
		{name: "access no owner", env: accessEnv(map[string]string{EnvAccessOwnerEmail: unset}), wantErr: errPartial, wantVar: EnvAccessOwnerEmail},
		{name: "access only owner", env: env(map[string]string{EnvAccessOwnerEmail: ownerEmail}), wantErr: errPartial, wantVar: EnvAccessTeamDomain + ", " + EnvAccessAudience},
		// Access: empty.
		{name: "access team domain empty", env: accessEnv(map[string]string{EnvAccessTeamDomain: ""}), wantErr: errEmpty, wantVar: EnvAccessTeamDomain},
		{name: "access aud empty", env: accessEnv(map[string]string{EnvAccessAudience: ""}), wantErr: errEmpty, wantVar: EnvAccessAudience},
		{name: "access owner empty", env: accessEnv(map[string]string{EnvAccessOwnerEmail: ""}), wantErr: errEmpty, wantVar: EnvAccessOwnerEmail},
		{name: "access owner only whitespace", env: accessEnv(map[string]string{EnvAccessOwnerEmail: " \n"}), wantErr: errEmpty, wantVar: EnvAccessOwnerEmail},
		{name: "access empty alone", env: env(map[string]string{EnvAccessAudience: ""}), wantErr: errEmpty, wantVar: EnvAccessAudience},
		// Access: team domain.
		{name: "team domain lowercased", env: accessEnv(map[string]string{EnvAccessTeamDomain: "ACME.CloudflareAccess.com"}), wantAccess: wantAccess},
		{name: "team domain any provider host", env: accessEnv(map[string]string{EnvAccessTeamDomain: "login.example.org"}),
			wantAccess: &Access{TeamDomain: "login.example.org", Audience: accessAUD, OwnerEmail: ownerEmail}},
		{name: "team domain scheme", env: accessEnv(map[string]string{EnvAccessTeamDomain: "https://" + teamDomain}), wantErr: errHostname, wantVar: EnvAccessTeamDomain},
		{name: "team domain port", env: accessEnv(map[string]string{EnvAccessTeamDomain: teamDomain + ":443"}), wantErr: errHostname, wantVar: EnvAccessTeamDomain},
		{name: "team domain path", env: accessEnv(map[string]string{EnvAccessTeamDomain: teamDomain + "/x"}), wantErr: errHostname, wantVar: EnvAccessTeamDomain},
		{name: "team domain trailing slash", env: accessEnv(map[string]string{EnvAccessTeamDomain: teamDomain + "/"}), wantErr: errHostname, wantVar: EnvAccessTeamDomain},
		{name: "team domain trailing dot", env: accessEnv(map[string]string{EnvAccessTeamDomain: teamDomain + "."}), wantErr: errHostname, wantVar: EnvAccessTeamDomain},
		{name: "team domain one label", env: accessEnv(map[string]string{EnvAccessTeamDomain: "acme"}), wantErr: errHostname, wantVar: EnvAccessTeamDomain},
		{name: "team domain ip", env: accessEnv(map[string]string{EnvAccessTeamDomain: "203.0.113.7"}), wantErr: errHostname, wantVar: EnvAccessTeamDomain},
		{name: "team domain space", env: accessEnv(map[string]string{EnvAccessTeamDomain: " " + teamDomain}), wantErr: errHostname, wantVar: EnvAccessTeamDomain},
		{name: "team domain leading hyphen", env: accessEnv(map[string]string{EnvAccessTeamDomain: "-acme.example.com"}), wantErr: errHostname, wantVar: EnvAccessTeamDomain},
		{name: "team domain trailing hyphen", env: accessEnv(map[string]string{EnvAccessTeamDomain: "acme-.example.com"}), wantErr: errHostname, wantVar: EnvAccessTeamDomain},
		{name: "team domain empty label", env: accessEnv(map[string]string{EnvAccessTeamDomain: "acme..com"}), wantErr: errHostname, wantVar: EnvAccessTeamDomain},
		{name: "team domain long label", env: accessEnv(map[string]string{EnvAccessTeamDomain: strings.Repeat("a", 64) + ".com"}), wantErr: errHostname, wantVar: EnvAccessTeamDomain},
		{name: "team domain 63-byte label", env: accessEnv(map[string]string{EnvAccessTeamDomain: strings.Repeat("a", 63) + ".com"}),
			wantAccess: &Access{TeamDomain: strings.Repeat("a", 63) + ".com", Audience: accessAUD, OwnerEmail: ownerEmail}},
		{name: "team domain too long", env: accessEnv(map[string]string{EnvAccessTeamDomain: strings.Repeat("a.", 127) + "co"}), wantErr: errHostname, wantVar: EnvAccessTeamDomain},
		{name: "team domain underscore", env: accessEnv(map[string]string{EnvAccessTeamDomain: "ac_me.example.com"}), wantErr: errHostname, wantVar: EnvAccessTeamDomain},
		{name: "team domain userinfo", env: accessEnv(map[string]string{EnvAccessTeamDomain: "a@" + teamDomain}), wantErr: errHostname, wantVar: EnvAccessTeamDomain},
		// Access: aud.
		{name: "aud space", env: accessEnv(map[string]string{EnvAccessAudience: "ab cd"}), wantErr: errSpace, wantVar: EnvAccessAudience},
		{name: "aud trailing newline", env: accessEnv(map[string]string{EnvAccessAudience: accessAUD + "\n"}), wantErr: errSpace, wantVar: EnvAccessAudience},
		{name: "aud control", env: accessEnv(map[string]string{EnvAccessAudience: "ab\x00cd"}), wantErr: errSpace, wantVar: EnvAccessAudience},
		// Access: owner email.
		{name: "owner email trimmed", env: accessEnv(map[string]string{EnvAccessOwnerEmail: " \t" + ownerEmail + "\n"}), wantAccess: wantAccess},
		{name: "owner email no at", env: accessEnv(map[string]string{EnvAccessOwnerEmail: "owner.example.com"}), wantErr: errEmail, wantVar: EnvAccessOwnerEmail},
		{name: "owner email two ats", env: accessEnv(map[string]string{EnvAccessOwnerEmail: "a@b@example.com"}), wantErr: errEmail, wantVar: EnvAccessOwnerEmail},
		{name: "owner email no local", env: accessEnv(map[string]string{EnvAccessOwnerEmail: "@example.com"}), wantErr: errEmail, wantVar: EnvAccessOwnerEmail},
		{name: "owner email no domain", env: accessEnv(map[string]string{EnvAccessOwnerEmail: "owner@"}), wantErr: errEmail, wantVar: EnvAccessOwnerEmail},
		{name: "owner email inner space", env: accessEnv(map[string]string{EnvAccessOwnerEmail: "own er@example.com"}), wantErr: errSpace, wantVar: EnvAccessOwnerEmail},

		// WorkOS: partial.
		{name: "workos no issuer", env: workosEnv(map[string]string{EnvWorkOSIssuer: unset}), wantErr: errPartial, wantVar: EnvWorkOSIssuer},
		{name: "workos no resource", env: workosEnv(map[string]string{EnvWorkOSResource: unset}), wantErr: errPartial, wantVar: EnvWorkOSResource},
		{name: "workos no owner", env: workosEnv(map[string]string{EnvWorkOSOwnerID: unset}), wantErr: errPartial, wantVar: EnvWorkOSOwnerID},
		{name: "workos only issuer", env: env(map[string]string{EnvWorkOSIssuer: issuer}), wantErr: errPartial, wantVar: EnvWorkOSResource + ", " + EnvWorkOSOwnerID},
		// WorkOS: empty.
		{name: "workos issuer empty", env: workosEnv(map[string]string{EnvWorkOSIssuer: ""}), wantErr: errEmpty, wantVar: EnvWorkOSIssuer},
		{name: "workos resource empty", env: workosEnv(map[string]string{EnvWorkOSResource: ""}), wantErr: errEmpty, wantVar: EnvWorkOSResource},
		{name: "workos owner empty", env: workosEnv(map[string]string{EnvWorkOSOwnerID: ""}), wantErr: errEmpty, wantVar: EnvWorkOSOwnerID},
		// WorkOS: issuer.
		{name: "issuer any provider host", env: workosEnv(map[string]string{EnvWorkOSIssuer: "https://auth.example.org"}),
			wantWorkOS: &WorkOS{Issuer: "https://auth.example.org", Resource: resource, OwnerSubject: ownerID}},
		{name: "issuer http", env: workosEnv(map[string]string{EnvWorkOSIssuer: "http://acme-42.authkit.app"}), wantErr: errHTTPSURL, wantVar: EnvWorkOSIssuer},
		{name: "issuer http loopback", env: workosEnv(map[string]string{EnvWorkOSIssuer: "http://127.0.0.1"}), wantErr: errHTTPSURL, wantVar: EnvWorkOSIssuer},
		{name: "issuer no scheme", env: workosEnv(map[string]string{EnvWorkOSIssuer: "acme-42.authkit.app"}), wantErr: errHTTPSURL, wantVar: EnvWorkOSIssuer},
		{name: "issuer no host", env: workosEnv(map[string]string{EnvWorkOSIssuer: "https:///x"}), wantErr: errHTTPSURL, wantVar: EnvWorkOSIssuer},
		{name: "issuer opaque", env: workosEnv(map[string]string{EnvWorkOSIssuer: "https:acme-42.authkit.app"}), wantErr: errHTTPSURL, wantVar: EnvWorkOSIssuer},
		{name: "issuer userinfo", env: workosEnv(map[string]string{EnvWorkOSIssuer: "https://u:p@acme-42.authkit.app"}), wantErr: errHTTPSURL, wantVar: EnvWorkOSIssuer},
		{name: "issuer query", env: workosEnv(map[string]string{EnvWorkOSIssuer: issuer + "?a=b"}), wantErr: errHTTPSURL, wantVar: EnvWorkOSIssuer},
		{name: "issuer empty query", env: workosEnv(map[string]string{EnvWorkOSIssuer: issuer + "?"}), wantErr: errHTTPSURL, wantVar: EnvWorkOSIssuer},
		{name: "issuer fragment", env: workosEnv(map[string]string{EnvWorkOSIssuer: issuer + "#f"}), wantErr: errHTTPSURL, wantVar: EnvWorkOSIssuer},
		{name: "issuer empty fragment", env: workosEnv(map[string]string{EnvWorkOSIssuer: issuer + "#"}), wantErr: errHTTPSURL, wantVar: EnvWorkOSIssuer},
		{name: "issuer unparseable", env: workosEnv(map[string]string{EnvWorkOSIssuer: "https://a b"}), wantErr: errHTTPSURL, wantVar: EnvWorkOSIssuer},
		{name: "issuer trailing slash", env: workosEnv(map[string]string{EnvWorkOSIssuer: issuer + "/"}), wantErr: errHasPath, wantVar: EnvWorkOSIssuer},
		{name: "issuer path", env: workosEnv(map[string]string{EnvWorkOSIssuer: issuer + "/oauth"}), wantErr: errHasPath, wantVar: EnvWorkOSIssuer},
		// WorkOS: canonical form, since claims are compared byte for byte.
		{name: "issuer non-default port", env: workosEnv(map[string]string{EnvWorkOSIssuer: "https://auth.example.org:8443"}),
			wantWorkOS: &WorkOS{Issuer: "https://auth.example.org:8443", Resource: resource, OwnerSubject: ownerID}},
		{name: "issuer v6 host", env: workosEnv(map[string]string{EnvWorkOSIssuer: "https://[2001:db8::1]"}),
			wantWorkOS: &WorkOS{Issuer: "https://[2001:db8::1]", Resource: resource, OwnerSubject: ownerID}},
		{name: "issuer upper scheme", env: workosEnv(map[string]string{EnvWorkOSIssuer: "HTTPS://acme-42.authkit.app"}), wantErr: errNotCanonical, wantVar: EnvWorkOSIssuer, wantIn: `"` + issuer + `"`},
		{name: "issuer upper host", env: workosEnv(map[string]string{EnvWorkOSIssuer: "https://ACME-42.AuthKit.app"}), wantErr: errNotCanonical, wantVar: EnvWorkOSIssuer, wantIn: `"` + issuer + `"`},
		{name: "issuer default port", env: workosEnv(map[string]string{EnvWorkOSIssuer: issuer + ":443"}), wantErr: errNotCanonical, wantVar: EnvWorkOSIssuer, wantIn: `"` + issuer + `"`},
		{name: "issuer default port leading zero", env: workosEnv(map[string]string{EnvWorkOSIssuer: issuer + ":0443"}), wantErr: errNotCanonical, wantVar: EnvWorkOSIssuer, wantIn: `"` + issuer + `"`},
		{name: "issuer port leading zero", env: workosEnv(map[string]string{EnvWorkOSIssuer: issuer + ":08443"}), wantErr: errNotCanonical, wantVar: EnvWorkOSIssuer, wantIn: `"` + issuer + `:8443"`},
		{name: "issuer empty port", env: workosEnv(map[string]string{EnvWorkOSIssuer: issuer + ":"}), wantErr: errNotCanonical, wantVar: EnvWorkOSIssuer, wantIn: `"` + issuer + `"`},
		{name: "issuer upper v6 host", env: workosEnv(map[string]string{EnvWorkOSIssuer: "https://[2001:DB8::1]"}), wantErr: errNotCanonical, wantVar: EnvWorkOSIssuer, wantIn: `"https://[2001:db8::1]"`},
		{name: "issuer v6 default port", env: workosEnv(map[string]string{EnvWorkOSIssuer: "https://[2001:db8::1]:443"}), wantErr: errNotCanonical, wantVar: EnvWorkOSIssuer, wantIn: `"https://[2001:db8::1]"`},
		{name: "issuer port zero", env: workosEnv(map[string]string{EnvWorkOSIssuer: issuer + ":0"}), wantErr: errHTTPSURL, wantVar: EnvWorkOSIssuer},
		{name: "issuer port above range", env: workosEnv(map[string]string{EnvWorkOSIssuer: issuer + ":65536"}), wantErr: errHTTPSURL, wantVar: EnvWorkOSIssuer},
		{name: "resource non-default port", env: workosEnv(map[string]string{EnvWorkOSResource: "https://memory.example.com:8443/api/mcp"}),
			wantWorkOS: &WorkOS{Issuer: issuer, Resource: "https://memory.example.com:8443/api/mcp", OwnerSubject: ownerID}},
		// The path is case-sensitive and kept as written.
		{name: "resource path case kept", env: workosEnv(map[string]string{EnvWorkOSResource: "https://memory.example.com/API/MCP"}),
			wantWorkOS: &WorkOS{Issuer: issuer, Resource: "https://memory.example.com/API/MCP", OwnerSubject: ownerID}},
		{name: "resource upper scheme", env: workosEnv(map[string]string{EnvWorkOSResource: "Https://memory.example.com/api/mcp"}), wantErr: errNotCanonical, wantVar: EnvWorkOSResource, wantIn: `"` + resource + `"`},
		{name: "resource upper host", env: workosEnv(map[string]string{EnvWorkOSResource: "https://Memory.Example.com/api/mcp"}), wantErr: errNotCanonical, wantVar: EnvWorkOSResource, wantIn: `"` + resource + `"`},
		{name: "resource default port", env: workosEnv(map[string]string{EnvWorkOSResource: "https://memory.example.com:443/api/mcp"}), wantErr: errNotCanonical, wantVar: EnvWorkOSResource, wantIn: `"` + resource + `"`},
		{name: "resource upper host and default port, path kept", env: workosEnv(map[string]string{EnvWorkOSResource: "https://MEMORY.example.com:443/Api/mcp"}),
			wantErr: errNotCanonical, wantVar: EnvWorkOSResource, wantIn: `"https://memory.example.com/Api/mcp"`},
		// WorkOS: resource.
		{name: "resource http", env: workosEnv(map[string]string{EnvWorkOSResource: "http://memory.example.com/api/mcp"}), wantErr: errHTTPSURL, wantVar: EnvWorkOSResource},
		{name: "resource userinfo", env: workosEnv(map[string]string{EnvWorkOSResource: "https://u@memory.example.com/api/mcp"}), wantErr: errHTTPSURL, wantVar: EnvWorkOSResource},
		{name: "resource query", env: workosEnv(map[string]string{EnvWorkOSResource: resource + "?a=b"}), wantErr: errHTTPSURL, wantVar: EnvWorkOSResource},
		{name: "resource fragment", env: workosEnv(map[string]string{EnvWorkOSResource: resource + "#f"}), wantErr: errHTTPSURL, wantVar: EnvWorkOSResource},
		{name: "resource no path", env: workosEnv(map[string]string{EnvWorkOSResource: "https://memory.example.com"}), wantErr: errNoPath, wantVar: EnvWorkOSResource},
		{name: "resource root path", env: workosEnv(map[string]string{EnvWorkOSResource: "https://memory.example.com/"}), wantErr: errNoPath, wantVar: EnvWorkOSResource},
		{name: "resource trailing slash", env: workosEnv(map[string]string{EnvWorkOSResource: resource + "/"}), wantErr: errTrailSlash, wantVar: EnvWorkOSResource},
		// WorkOS: owner ID.
		{name: "owner id space", env: workosEnv(map[string]string{EnvWorkOSOwnerID: "user 01"}), wantErr: errSpace, wantVar: EnvWorkOSOwnerID},
		{name: "owner id trailing newline", env: workosEnv(map[string]string{EnvWorkOSOwnerID: ownerID + "\n"}), wantErr: errSpace, wantVar: EnvWorkOSOwnerID},

		// A bad provider value fails start even when the rest is valid.
		{name: "workos error with access on", env: func() map[string]string {
			m := maps.Clone(both)
			m[EnvWorkOSOwnerID] = unset
			return env(m)
		}(), wantErr: errPartial, wantVar: EnvWorkOSOwnerID},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := LoadServe(lookupFrom(tc.env))
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("LoadServe() error = %v, want nil", err)
				}
				if !equalPtr(got.Access, tc.wantAccess) || !equalPtr(got.WorkOS, tc.wantWorkOS) {
					t.Fatalf("LoadServe() Access = %+v, WorkOS = %+v; want %+v, %+v", got.Access, got.WorkOS, tc.wantAccess, tc.wantWorkOS)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("LoadServe() error = %v, want errors.Is %v", err, tc.wantErr)
			}
			// The message leads with the variable(s) at fault; a partial
			// group's message lists the whole group too, so Contains would
			// prove nothing.
			if !strings.HasPrefix(err.Error(), tc.wantVar+" ") && !strings.HasPrefix(err.Error(), tc.wantVar+"=") {
				t.Errorf("LoadServe() error %q does not lead with %s", err, tc.wantVar)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("LoadServe() error %q does not contain %s", err, tc.wantIn)
			}
			// Owners are personal data: an error never echoes them.
			for _, v := range []string{"owner@", "own er", "user 01", ownerID} {
				if strings.Contains(err.Error(), v) {
					t.Errorf("LoadServe() error %q echoes owner value %q", err, v)
				}
			}
			if got != (Serve{}) {
				t.Errorf("LoadServe() = %#v on error, want zero", got)
			}
		})
	}
}

func equalPtr[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// TestOAuthNames pins the variable names, written out: deploy files and docs
// spell them.
func TestOAuthNames(t *testing.T) {
	t.Parallel()
	got := []string{EnvAccessTeamDomain, EnvAccessAudience, EnvAccessOwnerEmail, EnvWorkOSIssuer, EnvWorkOSResource, EnvWorkOSOwnerID}
	want := []string{"WENNA_ACCESS_TEAM_DOMAIN", "WENNA_ACCESS_AUD", "WENNA_ACCESS_OWNER_EMAIL", "WENNA_WORKOS_ISSUER", "WENNA_WORKOS_RESOURCE", "WENNA_WORKOS_OWNER_ID"}
	if !slices.Equal(got, want) {
		t.Errorf("names = %v, want %v", got, want)
	}
}

// TestLoadServePublicURL: WENNA_PUBLIC_URL is a canonical https origin, off
// when unset; with WorkOS on, the resource must be at that origin.
func TestLoadServePublicURL(t *testing.T) {
	t.Parallel()
	const pub = "https://memory.example.com"
	tests := []struct {
		name     string
		env      map[string]string
		want     string
		wantHost string
		wantErr  error
		wantVar  string
		wantIn   string
	}{
		{name: "unset", env: env(nil)},
		{name: "set", env: env(map[string]string{EnvPublicURL: pub}), want: pub, wantHost: "memory.example.com"},
		{name: "funnel host", env: env(map[string]string{EnvPublicURL: "https://mac.tail1234.ts.net"}), want: "https://mac.tail1234.ts.net", wantHost: "mac.tail1234.ts.net"},
		{name: "non-default port", env: env(map[string]string{EnvPublicURL: pub + ":8443"}), want: pub + ":8443", wantHost: "memory.example.com"},
		{name: "v6 host", env: env(map[string]string{EnvPublicURL: "https://[2001:db8::1]"}), want: "https://[2001:db8::1]", wantHost: "2001:db8::1"},
		{name: "v6 host, port", env: env(map[string]string{EnvPublicURL: "https://[2001:db8::1]:8443"}), want: "https://[2001:db8::1]:8443", wantHost: "2001:db8::1"},
		{name: "workos at the origin", env: workosEnv(map[string]string{EnvPublicURL: pub}), want: pub, wantHost: "memory.example.com"},
		{name: "workos at the origin, port", env: workosEnv(map[string]string{EnvPublicURL: pub + ":8443", EnvWorkOSResource: pub + ":8443/api/mcp"}), want: pub + ":8443", wantHost: "memory.example.com"},

		{name: "empty", env: env(map[string]string{EnvPublicURL: ""}), wantErr: errEmpty, wantVar: EnvPublicURL},
		{name: "http", env: env(map[string]string{EnvPublicURL: "http://memory.example.com"}), wantErr: errHTTPSURL, wantVar: EnvPublicURL},
		{name: "no scheme", env: env(map[string]string{EnvPublicURL: "memory.example.com"}), wantErr: errHTTPSURL, wantVar: EnvPublicURL},
		{name: "userinfo", env: env(map[string]string{EnvPublicURL: "https://u@memory.example.com"}), wantErr: errHTTPSURL, wantVar: EnvPublicURL},
		{name: "query", env: env(map[string]string{EnvPublicURL: pub + "?a=b"}), wantErr: errHTTPSURL, wantVar: EnvPublicURL},
		{name: "fragment", env: env(map[string]string{EnvPublicURL: pub + "#"}), wantErr: errHTTPSURL, wantVar: EnvPublicURL},
		{name: "trailing slash", env: env(map[string]string{EnvPublicURL: pub + "/"}), wantErr: errHasPath, wantVar: EnvPublicURL},
		{name: "path", env: env(map[string]string{EnvPublicURL: pub + "/api/mcp"}), wantErr: errHasPath, wantVar: EnvPublicURL},
		{name: "upper host", env: env(map[string]string{EnvPublicURL: "https://Memory.Example.com"}), wantErr: errNotCanonical, wantVar: EnvPublicURL, wantIn: `"` + pub + `"`},
		{name: "default port", env: env(map[string]string{EnvPublicURL: pub + ":443"}), wantErr: errNotCanonical, wantVar: EnvPublicURL, wantIn: `"` + pub + `"`},
		{name: "port zero", env: env(map[string]string{EnvPublicURL: pub + ":0"}), wantErr: errHTTPSURL, wantVar: EnvPublicURL},
		// One trusted origin: a resource elsewhere would advertise a second.
		{name: "workos other host", env: workosEnv(map[string]string{EnvPublicURL: "https://mac.tail1234.ts.net"}), wantErr: errOrigin, wantVar: EnvWorkOSResource, wantIn: "https://mac.tail1234.ts.net"},
		{name: "workos other port", env: workosEnv(map[string]string{EnvPublicURL: pub + ":8443"}), wantErr: errOrigin, wantVar: EnvWorkOSResource},
		{name: "workos port, public none", env: workosEnv(map[string]string{EnvPublicURL: pub, EnvWorkOSResource: pub + ":8443/api/mcp"}), wantErr: errOrigin, wantVar: EnvWorkOSResource},
		// A host that only starts with the public one is another origin.
		{name: "workos host prefix", env: workosEnv(map[string]string{EnvPublicURL: "https://memory.example.co"}), wantErr: errOrigin, wantVar: EnvWorkOSResource},
		{name: "workos error first", env: workosEnv(map[string]string{EnvPublicURL: pub, EnvWorkOSResource: ""}), wantErr: errEmpty, wantVar: EnvWorkOSResource},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := LoadServe(lookupFrom(tc.env))
			if tc.wantErr == nil {
				if err != nil || got.PublicURL != tc.want || got.PublicHost != tc.wantHost {
					t.Fatalf("LoadServe() PublicURL, PublicHost = %q, %q, %v; want %q, %q", got.PublicURL, got.PublicHost, err, tc.want, tc.wantHost)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("LoadServe() error = %v, want errors.Is %v", err, tc.wantErr)
			}
			if !strings.HasPrefix(err.Error(), tc.wantVar+" ") && !strings.HasPrefix(err.Error(), tc.wantVar+"=") {
				t.Errorf("LoadServe() error %q does not lead with %s", err, tc.wantVar)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("LoadServe() error %q does not contain %s", err, tc.wantIn)
			}
			if got != (Serve{}) {
				t.Errorf("LoadServe() = %#v on error, want zero", got)
			}
		})
	}
	// The name is written out: deploy files and docs spell it.
	if EnvPublicURL != "WENNA_PUBLIC_URL" {
		t.Errorf("EnvPublicURL = %q", EnvPublicURL)
	}
}

// TestDefaultsAreLocal pins the defaults, written out: each must stay on
// this machine.
func TestDefaultsAreLocal(t *testing.T) {
	t.Parallel()
	if DefaultListenHost != "127.0.0.1" || DefaultPort != "8080" || DefaultDB != ".wenna/wenna.db" {
		t.Errorf("defaults = %s:%s, %s", DefaultListenHost, DefaultPort, DefaultDB)
	}
}
