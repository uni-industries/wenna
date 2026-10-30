// Package httpapi is the HTTP surface of `wenna serve`. Every route lives
// under /api/, except the OAuth protected-resource metadata: RFC 9728 § 3
// fixes its path under /.well-known/, where MCP clients look for it.
package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/uni-industries/wenna/backend/internal/rebinding"
)

// MCPPath is the AI connector's path.
const MCPPath = "/api/mcp"

// healthBody is constant, so the handler never marshals.
const healthBody = `{"status":"ok"}`

// ExportPath is the path of the export download.
const ExportPath = "/api/export"

// ResourceMetadataPath is the RFC 9728 well-known path of the OAuth
// protected-resource metadata.
const ResourceMetadataPath = "/.well-known/oauth-protected-resource"

// Routes are the handlers New mounts, and what guards them.
type Routes struct {
	// MCP serves the AI connector.
	MCP http.Handler
	// Export serves the memory history as one file.
	Export http.Handler
	// ResourceMetadata serves the OAuth protected-resource metadata (RFC
	// 9728); nil leaves it unmounted. It is public and reads no user data.
	ResourceMetadata http.Handler
	// Protect admits only requests with the owner's proof. It guards MCP and
	// Export. Bearer-token requests are exempt from CSRF checks (CLAUDE.md §
	// Security invariants): a browser never attaches the proof on its own.
	Protect func(http.Handler) http.Handler
	// PublicHost is the one non-loopback Host a request arriving on loopback
	// may name, or "" for none; rebinding.Guard checks it on MCP and Export.
	PublicHost string
}

// New returns the API handler. Unmatched paths get the mux's 404; a known
// path with the wrong method gets its 405 with an Allow header.
func New(logger *slog.Logger, r Routes) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", health(logger))
	// Every protected route is behind the rebinding guard, inside the gate:
	// an unauthenticated request costs the gate's check and nothing more,
	// and the guard, a string comparison, runs only for the owner.
	guarded := func(h http.Handler) http.Handler { return r.Protect(rebinding.Guard(r.PublicHost, h)) }
	mux.Handle(MCPPath, guarded(r.MCP))
	mux.Handle("GET "+ExportPath, guarded(r.Export))
	// A GET pattern also matches HEAD, which would build a whole export to
	// discard it. HEAD is more specific than GET, so this route wins; the
	// methodless one gives every other method the same Allow.
	mux.HandleFunc("HEAD "+ExportPath, getOnly)
	mux.HandleFunc(ExportPath, getOnly)
	if r.ResourceMetadata != nil {
		// The bare form and the path-suffixed form for the resource MCPPath
		// (RFC 9728 § 3.1). OPTIONS lets a browser-based client preflight.
		for _, p := range []string{ResourceMetadataPath, ResourceMetadataPath + MCPPath} {
			mux.Handle("GET "+p, r.ResourceMetadata)
			mux.Handle("OPTIONS "+p, r.ResourceMetadata)
		}
	}
	return mux
}

// getOnly refuses a method on a GET-only route without doing any work.
func getOnly(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Allow", http.MethodGet)
	http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
}

func health(logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(healthBody)); err != nil {
			// Headers are sent; the client is gone. Logging is all that is left.
			logger.WarnContext(r.Context(), "write health response", "err", err)
		}
	}
}
