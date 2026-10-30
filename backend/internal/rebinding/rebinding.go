// Package rebinding refuses DNS-rebinding requests: a page on another site
// whose name resolves to loopback, reaching wenna through the user's browser.
package rebinding

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Guard is the MCP SDK's DNS-rebinding protection (go-sdk
// StreamableHTTPOptions.DisableLocalhostProtection), plus one allowed public
// host: a request that arrived on a loopback address must name a loopback
// Host, or publicHost when one is configured. A proxy that forwards to
// loopback without rewriting Host (Tailscale Funnel) sends the public name.
// Arrivals on any other address pass, as in the SDK. publicHost is a host
// name without port or IPv6 brackets, or "" for none.
func Guard(publicHost string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok &&
			isLoopback(local.String()) && !isLoopback(r.Host) && !isPublic(publicHost, r.Host) {
			// The SDK's response, word for word.
			http.Error(w, fmt.Sprintf("Forbidden: invalid Host header %q", r.Host), http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isPublic reports whether host, port stripped as isLoopback strips it, is
// publicHost. DNS names are case-insensitive.
func isPublic(publicHost, host string) bool {
	return publicHost != "" && strings.EqualFold(hostOnly(host), publicHost)
}

// isLoopback is the SDK's internal/util.IsLoopback, which cannot be
// imported: "localhost" exactly, or a loopback IP literal.
func isLoopback(addr string) bool {
	host := hostOnly(addr)
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return ip.IsLoopback()
}

// hostOnly strips a port, and the brackets of a bare IPv6 literal.
func hostOnly(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// No port: a bare host.
		return strings.Trim(addr, "[]")
	}
	return host
}
