package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// stub answers 200 naming itself, so a test sees which handler was reached.
func stub(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Test-Handler", name)
		w.WriteHeader(http.StatusOK)
	})
}

// testRoutes: Protect admits a request only if it carries X-Test-Token.
var testRoutes = Routes{
	MCP:    stub("mcp"),
	Export: stub("export"),
	Protect: func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Test-Token") == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	},
}

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		method          string
		path            string
		wantStatus      int
		wantContentType string
		wantBody        string
		wantAllow       string
	}{
		{
			name: "health", method: http.MethodGet, path: "/api/health",
			wantStatus: http.StatusOK, wantContentType: "application/json", wantBody: `{"status":"ok"}`,
		},
		{
			name: "health head", method: http.MethodHead, path: "/api/health",
			wantStatus: http.StatusOK, wantContentType: "application/json",
		},
		{name: "health post", method: http.MethodPost, path: "/api/health", wantStatus: http.StatusMethodNotAllowed, wantAllow: "GET, HEAD"},
		{name: "health delete", method: http.MethodDelete, path: "/api/health", wantStatus: http.StatusMethodNotAllowed, wantAllow: "GET, HEAD"},
		{name: "unknown api path", method: http.MethodGet, path: "/api/nope", wantStatus: http.StatusNotFound},
		{name: "health subpath", method: http.MethodGet, path: "/api/health/x", wantStatus: http.StatusNotFound},
		{name: "health outside api", method: http.MethodGet, path: "/health", wantStatus: http.StatusNotFound},
		{name: "root", method: http.MethodGet, path: "/", wantStatus: http.StatusNotFound},
	}

	h := New(slog.New(slog.DiscardHandler), testRoutes)

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
			res := rec.Result()
			body, err := io.ReadAll(res.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}

			if res.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.wantStatus)
			}
			if tc.wantContentType != "" {
				if got := res.Header.Get("Content-Type"); got != tc.wantContentType {
					t.Errorf("Content-Type = %q, want %q", got, tc.wantContentType)
				}
			}
			// The recorder keeps a HEAD body that a real server discards.
			if tc.method == http.MethodGet && tc.wantStatus == http.StatusOK && string(body) != tc.wantBody {
				t.Errorf("body = %q, want %q", body, tc.wantBody)
			}
			if got := res.Header.Get("Allow"); got != tc.wantAllow {
				t.Errorf("Allow = %q, want %q", got, tc.wantAllow)
			}
		})
	}
}

// TestResourceMetadata mounts the handler cmd/wenna passes, and checks it is
// public: served without the owner's proof, never behind Protect.
func TestResourceMetadata(t *testing.T) {
	t.Parallel()
	const resource = "https://wenna.example/api/mcp"
	r := testRoutes
	r.ResourceMetadata = auth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
		Resource:             resource,
		AuthorizationServers: []string{"https://issuer.example"},
	})
	h := New(slog.New(slog.DiscardHandler), r)

	tests := []struct {
		name, method, path string
		wantStatus         int
		wantAllow          string
	}{
		{"bare", http.MethodGet, ResourceMetadataPath, http.StatusOK, ""},
		{"path-suffixed for mcp", http.MethodGet, ResourceMetadataPath + MCPPath, http.StatusOK, ""},
		{"preflight", http.MethodOptions, ResourceMetadataPath + MCPPath, http.StatusNoContent, ""},
		{"post", http.MethodPost, ResourceMetadataPath, http.StatusMethodNotAllowed, "GET, HEAD, OPTIONS"},
		// HEAD matches the GET pattern; the handler itself refuses it.
		{"head", http.MethodHead, ResourceMetadataPath, http.StatusMethodNotAllowed, ""},
		{"other resource", http.MethodGet, ResourceMetadataPath + ExportPath, http.StatusNotFound, ""},
		{"subpath", http.MethodGet, ResourceMetadataPath + MCPPath + "/x", http.StatusNotFound, ""},
		// Mounting the metadata must not open the protected routes.
		{"mcp still protected", http.MethodPost, MCPPath, http.StatusUnauthorized, ""},
		{"export still protected", http.MethodGet, ExportPath, http.StatusUnauthorized, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body %q", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if got := rec.Header().Get("Allow"); got != tc.wantAllow {
				t.Errorf("Allow = %q, want %q", got, tc.wantAllow)
			}
			if tc.wantStatus != http.StatusOK {
				return
			}
			if got := rec.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", got)
			}
			var got oauthex.ProtectedResourceMetadata
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode metadata: %v; body %q", err, rec.Body.String())
			}
			if got.Resource != resource {
				t.Errorf("resource = %q, want %q", got.Resource, resource)
			}
		})
	}
}

// TestRebindingGuard: every protected route refuses a rebinding Host on
// loopback, admits localhost and the public host, and runs the guard only
// after the gate.
func TestRebindingGuard(t *testing.T) {
	t.Parallel()
	const public = "mac.tail1234.ts.net"
	r := testRoutes
	r.PublicHost = public
	h := New(slog.New(slog.DiscardHandler), r)
	loopback := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8080}

	routes := []struct{ method, path, handler string }{
		{http.MethodPost, MCPPath, "mcp"},
		{http.MethodGet, ExportPath, "export"},
	}
	hosts := []struct {
		name, host  string
		token       bool
		wantStatus  int
		wantReached bool
	}{
		{"localhost", "localhost:8080", true, http.StatusOK, true},
		{"public host", public, true, http.StatusOK, true},
		{"rebinding", "evil.example", true, http.StatusForbidden, false},
		// The gate runs first: no token costs only its check.
		{"rebinding without token", "evil.example", false, http.StatusUnauthorized, false},
	}
	for _, rt := range routes {
		for _, tc := range hosts {
			t.Run(rt.handler+" "+tc.name, func(t *testing.T) {
				t.Parallel()
				req := httptest.NewRequest(rt.method, rt.path, strings.NewReader("{}"))
				req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, loopback))
				req.Host = tc.host
				if tc.token {
					req.Header.Set("X-Test-Token", "t")
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if rec.Code != tc.wantStatus {
					t.Fatalf("status = %d, want %d; body %q", rec.Code, tc.wantStatus, rec.Body.String())
				}
				if reached := rec.Header().Get("X-Test-Handler") == rt.handler; reached != tc.wantReached {
					t.Errorf("handler reached = %v, want %v", reached, tc.wantReached)
				}
			})
		}
	}
}

// failingWriter stands in for a client that disconnected after the headers.
type failingWriter struct{ header http.Header }

func (f *failingWriter) Header() http.Header       { return f.header }
func (f *failingWriter) WriteHeader(int)           {}
func (f *failingWriter) Write([]byte) (int, error) { return 0, errors.New("client gone") }

func TestHealthLogsWriteError(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	h := New(slog.New(slog.NewTextHandler(&logs, nil)), testRoutes)

	h.ServeHTTP(&failingWriter{header: http.Header{}}, httptest.NewRequest(http.MethodGet, "/api/health", nil))

	if got := logs.String(); !strings.Contains(got, "client gone") {
		t.Errorf("log = %q, want the write error logged", got)
	}
}

func TestRoutes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		method      string
		path        string
		token       string
		wantStatus  int
		wantHandler string
		wantAllow   string
	}{
		{name: "mcp with token", method: http.MethodPost, path: "/api/mcp", token: "t", wantStatus: http.StatusOK, wantHandler: "mcp"},
		{name: "mcp without token", method: http.MethodPost, path: "/api/mcp", wantStatus: http.StatusUnauthorized},
		{name: "mcp get without token", method: http.MethodGet, path: "/api/mcp", wantStatus: http.StatusUnauthorized},
		{name: "mcp subpath", method: http.MethodPost, path: "/api/mcp/x", token: "t", wantStatus: http.StatusNotFound},
		{name: "export with token", method: http.MethodGet, path: "/api/export", token: "t", wantStatus: http.StatusOK, wantHandler: "export"},
		{name: "export without token", method: http.MethodGet, path: "/api/export", wantStatus: http.StatusUnauthorized},
		// HEAD would otherwise match GET and build a whole export to discard.
		{name: "export head", method: http.MethodHead, path: "/api/export", token: "t", wantStatus: http.StatusMethodNotAllowed, wantAllow: "GET"},
		{name: "export head without token", method: http.MethodHead, path: "/api/export", wantStatus: http.StatusMethodNotAllowed, wantAllow: "GET"},
		{name: "export post", method: http.MethodPost, path: "/api/export", token: "t", wantStatus: http.StatusMethodNotAllowed, wantAllow: "GET"},
		{name: "export delete", method: http.MethodDelete, path: "/api/export", token: "t", wantStatus: http.StatusMethodNotAllowed, wantAllow: "GET"},
		{name: "metadata unmounted", method: http.MethodGet, path: ResourceMetadataPath, wantStatus: http.StatusNotFound},
		{name: "metadata for mcp unmounted", method: http.MethodGet, path: ResourceMetadataPath + MCPPath, wantStatus: http.StatusNotFound},
	}
	h := New(slog.New(slog.DiscardHandler), testRoutes)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
			if tc.token != "" {
				req.Header.Set("X-Test-Token", tc.token)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body %q", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if got := rec.Header().Get("X-Test-Handler"); got != tc.wantHandler {
				t.Errorf("handler = %q, want %q", got, tc.wantHandler)
			}
			if got := rec.Header().Get("Allow"); got != tc.wantAllow {
				t.Errorf("Allow = %q, want %q", got, tc.wantAllow)
			}
		})
	}
}
