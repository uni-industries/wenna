package rebinding

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const funnelHost = "mac.tail1234.ts.net"

var (
	loopback4 = &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8080}
	loopback6 = &net.TCPAddr{IP: net.IPv6loopback, Port: 8080}
	external  = &net.TCPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 8080}
)

// ok answers 200, standing in for a protected route.
var ok = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

// forbidden reports whether h refused a request for host that arrived on
// local (nil: no local address in the context) with the rebinding 403.
func forbidden(t *testing.T, h http.Handler, local net.Addr, host string) bool {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	r.Host = host
	if local != nil {
		r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, local))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code == http.StatusForbidden && strings.Contains(w.Body.String(), "invalid Host header")
}

func TestGuard(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		publicHost string
		local      net.Addr
		host       string
		want403    bool
	}{
		{"localhost", "", loopback4, "localhost", false},
		{"localhost port", "", loopback4, "localhost:8080", false},
		{"v4 loopback port", "", loopback4, "127.0.0.1:8080", false},
		{"other v4 loopback", "", loopback4, "127.0.0.2", false},
		{"v6 loopback bracketed", "", loopback4, "[::1]", false},
		{"v6 loopback port", "", loopback6, "[::1]:8080", false},
		{"v6 loopback bare", "", loopback6, "::1", false},
		{"localhost with public set", funnelHost, loopback4, "localhost:8080", false},

		// DNS rebinding: a page on evil.example resolves it to 127.0.0.1.
		{"rebinding", "", loopback4, "evil.example", true},
		{"rebinding port", "", loopback4, "evil.example:8080", true},
		{"rebinding v6 arrival", "", loopback6, "evil.example", true},
		{"rebinding with public set", funnelHost, loopback4, "evil.example", true},
		{"empty host", funnelHost, loopback4, "", true},
		// The SDK matches "localhost" exactly.
		{"upper localhost", "", loopback4, "LOCALHOST", true},
		{"localhost trailing dot", "", loopback4, "localhost.", true},
		{"non-loopback ip", "", loopback4, "192.0.2.1", true},

		// Unconfigured, as the SDK did: the Funnel host is refused.
		{"public unconfigured", "", loopback4, funnelHost, true},
		{"public", funnelHost, loopback4, funnelHost, false},
		{"public port", funnelHost, loopback4, funnelHost + ":443", false},
		{"public v6 arrival", funnelHost, loopback6, funnelHost, false},
		{"public case", funnelHost, loopback4, "MAC.Tail1234.TS.net", false},
		{"public trailing dot", funnelHost, loopback4, funnelHost + ".", true},
		{"public subdomain", funnelHost, loopback4, "evil." + funnelHost, true},
		{"public suffix added", funnelHost, loopback4, funnelHost + ".evil.example", true},
		{"public prefix", funnelHost, loopback4, "mac.tail1234.ts.ne", true},
		{"public in port", funnelHost, loopback4, "evil.example:" + funnelHost, true},
		{"public v6", "2001:db8::1", loopback4, "[2001:db8::1]", false},
		{"public v6 port", "2001:db8::1", loopback4, "[2001:db8::1]:443", false},
		{"public v6 case", "2001:db8::1", loopback4, "[2001:DB8::1]", false},
		{"public v6 other", "2001:db8::1", loopback4, "[2001:db8::2]", true},

		// Arrivals on another address are the proxy's concern, not this check.
		{"external arrival", "", external, "evil.example", false},
		{"external arrival public", funnelHost, external, "evil.example", false},
		{"no local addr", "", nil, "evil.example", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := forbidden(t, Guard(tc.publicHost, ok), tc.local, tc.host); got != tc.want403 {
				t.Errorf("Host %q on %v, public %q: forbidden = %v, want %v", tc.host, tc.local, tc.publicHost, got, tc.want403)
			}
		})
	}
}

// TestGuardMatchesSDK: with no public host, Guard refuses exactly what the
// MCP SDK's own protection refuses.
func TestGuardMatchesSDK(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.DiscardHandler)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sdk := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, Logger: logger,
	})
	ours := Guard("", ok)
	hosts := []string{
		"localhost", "localhost:1", "LOCALHOST", "Localhost:8080", "localhost.", "127.0.0.1", "127.0.0.1:8080",
		"127.1.2.3", "[::1]", "[::1]:8080", "::1", "[::ffff:127.0.0.1]", "::ffff:127.0.0.1", "[::1%25lo0]",
		"0.0.0.0", "[::]", "192.0.2.1", "evil.example", "evil.example:80", funnelHost, "", "[", "]", ":", "[]:80",
		"localhost:", "localhost:x", "127.0.0.1.evil.example", "localhost.evil.example",
	}
	for _, local := range []net.Addr{loopback4, loopback6, external, nil} {
		for _, host := range hosts {
			want := forbidden(t, sdk, local, host)
			if got := forbidden(t, ours, local, host); got != want {
				t.Errorf("Host %q on %v: forbidden = %v, SDK %v", host, local, got, want)
			}
		}
	}
}

// TestGuardListener: through a real loopback listener, which sets the local
// address, as Tailscale Funnel reaches wenna.
func TestGuardListener(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		publicHost string
		want       int
	}{
		{"", http.StatusForbidden},
		{funnelHost, http.StatusOK},
	} {
		srv := httptest.NewServer(Guard(tc.publicHost, ok))
		t.Cleanup(srv.Close)
		req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = funnelHost
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(resp.Body)
		if cErr := resp.Body.Close(); cErr != nil {
			t.Fatal(cErr)
		}
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != tc.want {
			t.Errorf("public %q, Host %q: status %d, want %d; body %s", tc.publicHost, funnelHost, resp.StatusCode, tc.want, b)
		}
	}
}
