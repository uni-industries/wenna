package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/uni-industries/wenna/backend/internal/config"
	"github.com/uni-industries/wenna/backend/internal/gate"
	"github.com/uni-industries/wenna/backend/internal/id"
	"github.com/uni-industries/wenna/backend/internal/sqlitestore"
)

// TestProtection checks the wiring from configuration to the gate: OAuth
// providers are off unless configured, and WorkOS turns on the metadata and
// the challenge that points at it.
// A WorkOS resource on another path would advertise metadata that answers
// 404, so it fails at start.
func TestProtectionResourcePath(t *testing.T) {
	t.Parallel()
	for _, resource := range []string{"https://memory.example.com/mcp", "https://memory.example.com/wenna/api/mcp"} {
		env := map[string]string{
			"HOME": t.TempDir(), config.EnvToken: testToken,
			config.EnvWorkOSIssuer: "https://auth.example.com", config.EnvWorkOSResource: resource, config.EnvWorkOSOwnerID: "user_01",
		}
		cfg, err := config.LoadServe(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := protection(cfg, &http.Client{}, slog.New(slog.DiscardHandler)); err == nil || !strings.Contains(err.Error(), "https://memory.example.com/api/mcp") {
			t.Errorf("resource %q: err = %v, want a refusal naming the path to use", resource, err)
		}
	}
}

func TestProtection(t *testing.T) {
	t.Parallel()
	const resource = "https://memory.example.com/api/mcp"
	const metaURL = "https://memory.example.com/.well-known/oauth-protected-resource/api/mcp"
	base := map[string]string{"HOME": t.TempDir(), config.EnvToken: testToken}
	workos := map[string]string{
		config.EnvWorkOSIssuer:   "https://auth.example.com",
		config.EnvWorkOSResource: resource,
		config.EnvWorkOSOwnerID:  "user_01",
	}
	access := map[string]string{
		config.EnvAccessTeamDomain: "team.example.com",
		config.EnvAccessAudience:   "aud-tag",
		config.EnvAccessOwnerEmail: "owner@example.com",
	}
	tests := []struct {
		name      string
		env       []map[string]string
		wantMeta  bool
		challenge string
	}{
		{name: "token only", env: nil, challenge: `Bearer realm="wenna"`},
		{name: "access", env: []map[string]string{access}, challenge: `Bearer realm="wenna"`},
		{name: "workos", env: []map[string]string{workos}, wantMeta: true,
			challenge: `Bearer realm="wenna", resource_metadata="` + metaURL + `"`},
		{name: "both", env: []map[string]string{access, workos}, wantMeta: true,
			challenge: `Bearer realm="wenna", resource_metadata="` + metaURL + `"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := map[string]string{}
			for _, m := range append([]map[string]string{base}, tc.env...) {
				for k, v := range m {
					env[k] = v
				}
			}
			cfg, err := config.LoadServe(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
			if err != nil {
				t.Fatal(err)
			}
			protect, meta, err := protection(cfg, &http.Client{}, slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatal(err)
			}
			if (meta != nil) != tc.wantMeta {
				t.Fatalf("metadata handler set = %v, want %v", meta != nil, tc.wantMeta)
			}
			rec := httptest.NewRecorder()
			protect(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("refused request reached the handler") })).
				ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/mcp", nil))
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
			if got := rec.Header().Get("WWW-Authenticate"); got != tc.challenge {
				t.Errorf("challenge = %q, want %q", got, tc.challenge)
			}
			if meta == nil {
				return
			}
			rec = httptest.NewRecorder()
			meta.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource/api/mcp", nil))
			var got struct {
				Resource             string   `json:"resource"`
				AuthorizationServers []string `json:"authorization_servers"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("metadata %q: %v", rec.Body.String(), err)
			}
			if got.Resource != resource || len(got.AuthorizationServers) != 1 || got.AuthorizationServers[0] != "https://auth.example.com" {
				t.Errorf("metadata = %+v", got)
			}
		})
	}
}

const testToken = "e2e-token-0123456789abcdef0123456789abcdef"

// binPath is the wenna binary TestMain builds, so tests drive it the way an AI
// app does; buildErr is why it is missing.
var binPath, buildErr = "", error(nil)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "wenna-e2e-")
	if err != nil {
		buildErr = err
	} else {
		binPath = filepath.Join(dir, "wenna")
		if out, err := exec.Command("go", "build", "-o", binPath, ".").CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("%w: %s", err, out)
		}
	}
	code := m.Run()
	if dir != "" {
		if err := os.RemoveAll(dir); err != nil {
			log.Print("remove test binary: ", err)
			code = 1
		}
	}
	os.Exit(code)
}

func wennaBinary(t *testing.T) string {
	t.Helper()
	if buildErr != nil {
		t.Fatalf("build wenna: %v", buildErr)
	}
	return binPath
}

func stdioClient(t *testing.T, bin, db string) *mcp.ClientSession {
	t.Helper()
	cmd := exec.Command(bin, "stdio")
	cmd.Env = []string{"WENNA_DB=" + db}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0"}, nil).Connect(context.Background(), &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("start wenna stdio: %v", err)
	}
	t.Cleanup(func() {
		if err := cs.Close(); err != nil {
			t.Errorf("close stdio session: %v", err)
		}
	})
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s: tool error %v", name, res.Content)
	}
	js, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	return string(js)
}

func gunzip(t *testing.T, r io.Reader) string {
	t.Helper()
	zr, err := gzip.NewReader(r)
	if err != nil {
		t.Fatalf("not gzip: %v", err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestOneMemoryForAllAIs: two AI apps, each running its own `wenna stdio`,
// and the HTTP server share one database file; what one remembers, every
// other recalls, and both exports carry it.
func TestOneMemoryForAllAIs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bin := wennaBinary(t)
	db := filepath.Join(t.TempDir(), "wenna.db")

	first, second := stdioClient(t, bin, db), stdioClient(t, bin, db)
	fact := "locker code is " + id.New()
	call(t, first, "remember", map[string]any{"content": fact})
	if got := call(t, second, "recall", map[string]any{"query": "locker code"}); !strings.Contains(got, fact) {
		t.Errorf("second app recall = %s, want the first app's memory", got)
	}

	store, err := sqlitestore.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	discard := slog.New(slog.DiscardHandler)
	api := httptest.NewServer(newHandler(store, "", gate.Require(gate.Options{Token: testToken, Logger: discard}), nil, discard))
	t.Cleanup(api.Close)

	for _, path := range []string{"/api/mcp", "/api/export"} {
		req, err := http.NewRequest(http.MethodGet, api.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer wrong-token")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if err := res.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s with a wrong token: %d, want 401", path, res.StatusCode)
		}
	}

	httpClient := &http.Client{Transport: bearerTransport{tok: testToken}}
	web, err := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0"}, nil).Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: api.URL + "/api/mcp", HTTPClient: httpClient, DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatalf("connect over HTTP: %v", err)
	}
	t.Cleanup(func() {
		if err := web.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	if got := call(t, web, "list", map[string]any{}); !strings.Contains(got, fact) {
		t.Errorf("HTTP list = %s, want the stdio app's memory", got)
	}

	res, err := httpClient.Get(api.URL + "/api/export")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := res.Body.Close(); err != nil {
			t.Error(err)
		}
	}()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("HTTP export: %d", res.StatusCode)
	}
	if got := gunzip(t, res.Body); !strings.Contains(got, fact) {
		t.Errorf("HTTP export lacks the memory: %s", got)
	}

	cmd := exec.Command(bin, "export")
	cmd.Env = []string{"WENNA_DB=" + db}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("wenna export: %v", err)
	}
	if got := gunzip(t, bytes.NewReader(out)); !strings.Contains(got, fact) || !strings.Contains(got, `"format":"wenna-export"`) {
		t.Errorf("CLI export lacks the memory: %s", got)
	}
}

// TestTrigramFlag: WENNA_TRIGRAM reaches the store. Unset or true, opening the
// database creates the trigram index; false, it never gets one.
func TestTrigramFlag(t *testing.T) {
	t.Parallel()
	bin := wennaBinary(t)
	for _, tc := range []struct {
		env  []string
		want int
	}{
		{nil, 1},
		{[]string{"WENNA_TRIGRAM=false"}, 0},
		{[]string{"WENNA_TRIGRAM=true"}, 1},
	} {
		db := filepath.Join(t.TempDir(), "wenna.db")
		cmd := exec.Command(bin, "export")
		cmd.Env = append([]string{"WENNA_DB=" + db}, tc.env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%q wenna export: %v\n%s", tc.env, err, bytes.TrimSpace(out))
		}
		conn, err := sql.Open("sqlite3", db)
		if err != nil {
			t.Fatal(err)
		}
		var n int
		err = conn.QueryRow("SELECT count(*) FROM sqlite_master WHERE name = 'memory_trigram'").Scan(&n)
		if cErr := conn.Close(); err != nil || cErr != nil {
			t.Fatal(errors.Join(err, cErr))
		}
		if n != tc.want {
			t.Errorf("%q: memory_trigram tables = %d, want %d", tc.env, n, tc.want)
		}
	}
}

// TestCommandLine: mistakes exit non-zero with a message naming what to fix.
func TestCommandLine(t *testing.T) {
	t.Parallel()
	bin := wennaBinary(t)
	db := filepath.Join(t.TempDir(), "wenna.db")
	tests := []struct {
		name     string
		args     []string
		env      []string
		wantCode int
		wantErr  string
	}{
		{name: "no command", wantCode: 2, wantErr: "usage: wenna"},
		{name: "unknown command", args: []string{"nope"}, wantCode: 2, wantErr: "usage: wenna"},
		{name: "serve without token", args: []string{"serve"}, env: []string{"WENNA_DB=" + db}, wantCode: 1, wantErr: "WENNA_TOKEN"},
		{name: "serve beyond loopback", args: []string{"serve"}, env: []string{"WENNA_DB=" + db, "WENNA_TOKEN=" + testToken, "WENNA_LISTEN_HOST=0.0.0.0"}, wantCode: 1, wantErr: "REFUSED: wenna serve speaks plain HTTP"},
		// A volume that is not mounted: refuse, never create a throwaway directory.
		{name: "database directory missing", args: []string{"stdio"}, env: []string{"WENNA_DB=" + filepath.Join(filepath.Dir(db), "unmounted", "wenna.db")}, wantCode: 1, wantErr: "mount a persistent volume"},
		{name: "relative database", args: []string{"stdio"}, env: []string{"WENNA_DB=rel.db"}, wantCode: 1, wantErr: "WENNA_DB"},
		{name: "no home, no database", args: []string{"export"}, wantCode: 1, wantErr: "WENNA_DB"},
		{name: "trigram not a bool", args: []string{"export"}, env: []string{"WENNA_DB=" + db, "WENNA_TRIGRAM=yes"}, wantCode: 1, wantErr: "WENNA_TRIGRAM"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Bounded: a command that should refuse to start but serves instead
			// is killed and fails here, never left listening.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, tc.args...)
			// Never nil: a nil Env inherits this process's, HOME included.
			cmd.Env = append([]string{}, tc.env...)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err := cmd.Run()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != tc.wantCode {
				t.Fatalf("exit = %v, want code %d; stderr %q", err, tc.wantCode, stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.wantErr) {
				t.Errorf("stderr = %q, want it to name %q", stderr.String(), tc.wantErr)
			}
		})
	}
}

// TestVersion: `wenna version` prints "dev" from a plain build and the
// injected tag from a release build, which sets it the same way.
func TestVersion(t *testing.T) {
	t.Parallel()
	release := filepath.Join(t.TempDir(), "wenna")
	const tag = "v9.8.7-test"
	if out, err := exec.Command("go", "build", "-ldflags", "-X main.version="+tag, "-o", release, ".").CombinedOutput(); err != nil {
		t.Fatalf("release build: %v: %s", err, out)
	}
	for bin, want := range map[string]string{wennaBinary(t): "dev", release: tag} {
		cmd := exec.Command(bin, "version")
		cmd.Env = []string{}
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s version: %v", bin, err)
		}
		if got := string(out); got != want+"\n" {
			t.Errorf("%s version = %q, want %q", bin, got, want+"\n")
		}
	}
}

// started is a wenna process bounded by a 30s deadline. t.Cleanup kills it and
// waits, so no run, a mutant's included, is left behind.
type started struct {
	cmd    *exec.Cmd
	stderr *bytes.Buffer
	exited chan struct{}
	err    error // cmd.Wait's result; read only after exited closes
}

func start(t *testing.T, cmd *exec.Cmd) *started {
	t.Helper()
	p := &started{cmd: cmd, stderr: &bytes.Buffer{}, exited: make(chan struct{})}
	cmd.Stderr = p.stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start wenna: %v", err)
	}
	go func() { p.err = cmd.Wait(); close(p.exited) }()
	return p
}

// command is the binary run with exactly env (never nil: a nil Env inherits
// HOME) and killed at the deadline or at cleanup.
func command(t *testing.T, env []string, args ...string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, wennaBinary(t), args...)
	cmd.Env = append([]string{}, env...)
	return cmd
}

// stopWithSIGTERM signals the process and waits for it; its exit must be 0
// with no "wenna:" failure line.
func (p *started) stopWithSIGTERM(t *testing.T) string {
	t.Helper()
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal: %v", err)
	}
	<-p.exited
	stderr := p.stderr.String()
	if p.err != nil {
		t.Fatalf("exit after SIGTERM = %v, want 0; stderr %q", p.err, stderr)
	}
	if strings.Contains(stderr, "wenna:") {
		t.Errorf("stderr after SIGTERM reports a failure: %q", stderr)
	}
	return stderr
}

// TestStdioStopsOnSignal: an AI app stops `wenna stdio` with a signal; that is
// a normal shutdown, exit 0, not a crash.
func TestStdioStopsOnSignal(t *testing.T) {
	t.Parallel()
	cmd := command(t, []string{"WENNA_DB=" + filepath.Join(t.TempDir(), "wenna.db")}, "stdio")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	p := start(t, cmd)
	t.Cleanup(func() {
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Errorf("kill: %v", err)
		}
		<-p.exited
	})

	// Up means it answered initialize; stdin stays open so EOF cannot be
	// what stops it.
	const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"e2e","version":"0"}}}` + "\n"
	if _, err := io.WriteString(stdin, initialize); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || !strings.Contains(line, `"id":1`) || !strings.Contains(line, `"result"`) {
		t.Fatalf("initialize response = %q, %v", line, err)
	}
	p.stopWithSIGTERM(t)
}

// TestServe drives the real `wenna serve`: environment, config, listen, token,
// and a clean stop on SIGTERM.
func TestServe(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	// The token as a pasted value often arrives: with a trailing newline.
	cmd := command(t, []string{
		"WENNA_DB=" + filepath.Join(t.TempDir(), "wenna.db"),
		"WENNA_TOKEN=" + testToken + "\n",
		fmt.Sprintf("PORT=%d", port),
		"WENNA_PUBLIC_URL=https://" + publicHostName,
	}, "serve")
	p := start(t, cmd)
	t.Cleanup(func() {
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Errorf("kill: %v", err)
		}
		<-p.exited
	})

	waitHealthy(t, p, base+"/api/health")

	req, err := http.NewRequest(http.MethodPost, base+"/api/mcp", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("/api/mcp without token: %d, want 401", res.StatusCode)
	}

	web, err := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0"}, nil).Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: base + "/api/mcp", HTTPClient: &http.Client{Transport: bearerTransport{tok: testToken}}, DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatalf("connect with the token: %v", err)
	}
	fact := "parking spot is " + id.New()
	call(t, web, "remember", map[string]any{"content": fact})
	if got := call(t, web, "list", map[string]any{}); !strings.Contains(got, fact) {
		t.Errorf("list = %s, want the memory just saved", got)
	}
	if err := web.Close(); err != nil {
		t.Errorf("close: %v", err)
	}

	// A proxy forwarding to loopback with the public Host (Tailscale Funnel)
	// is admitted; any other name on loopback is DNS rebinding.
	// The guard covers every protected route, the export included.
	for host, want := range map[string]int{publicHostName: http.StatusOK, "evil.example": http.StatusForbidden} {
		if got := initializeStatus(t, base+"/api/mcp", host); got != want {
			t.Errorf("/api/mcp Host %q: status %d, want %d", host, got, want)
		}
	}
	for host, want := range map[string]int{
		fmt.Sprintf("localhost:%d", port): http.StatusOK, publicHostName: http.StatusOK, "evil.example": http.StatusForbidden,
	} {
		if got := exportStatus(t, base+"/api/export", host); got != want {
			t.Errorf("/api/export Host %q: status %d, want %d", host, got, want)
		}
	}

	stderr := p.stopWithSIGTERM(t)
	if !strings.Contains(stderr, "server stopped") {
		t.Errorf("log = %q, want it to say the server stopped", stderr)
	}
	if !strings.Contains(stderr, "config.public_url=https://"+publicHostName) {
		t.Errorf("log = %q, want the public URL", stderr)
	}
}

const publicHostName = "mac.tail1234.ts.net"

// initializeStatus posts an authenticated MCP initialize to url with Host
// set to host and returns the status.
func initializeStatus(t *testing.T, url, host string) int {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Body.Close(); err != nil {
		t.Fatal(err)
	}
	return res.StatusCode
}

// exportStatus gets the export from url with the token and Host set to
// host, and returns the status.
func exportStatus(t *testing.T, url, host string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	req.Header.Set("Authorization", "Bearer "+testToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, res.Body); err != nil {
		t.Fatal(err)
	}
	if err := res.Body.Close(); err != nil {
		t.Fatal(err)
	}
	return res.StatusCode
}

// waitHealthy polls url until 200, failing if the process exits first or
// 10s pass.
func waitHealthy(t *testing.T, p *started, url string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case <-p.exited:
			t.Fatalf("wenna serve exited before healthy: %v; stderr %q", p.err, p.stderr.String())
		default:
		}
		res, err := http.Get(url)
		if err == nil {
			if cErr := res.Body.Close(); cErr != nil {
				t.Fatal(cErr)
			}
			if res.StatusCode == http.StatusOK {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("not healthy after 10s: last %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// bearerTransport adds the token to every request.
type bearerTransport struct{ tok string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.tok)
	return http.DefaultTransport.RoundTrip(r)
}
