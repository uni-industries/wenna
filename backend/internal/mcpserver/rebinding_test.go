package mcpserver

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandlerLeavesHostToGuard: Handler turns the SDK's own Host check off,
// so a public host reaching loopback through a proxy is not refused here.
// rebinding.Guard, mounted by httpapi, is the one check.
func TestHandlerLeavesHostToGuard(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.DiscardHandler)
	srv := httptest.NewServer(Handler(Server(&fakeStore{}, logger), logger))
	t.Cleanup(srv.Close)
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "mac.tail1234.ts.net"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
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
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Host %q on loopback: status %d, want 200; body %s", req.Host, resp.StatusCode, b)
	}
}
