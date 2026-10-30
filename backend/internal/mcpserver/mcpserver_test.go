package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/uni-industries/wenna/backend/internal/memory"
)

// fakeStore counts calls and returns scripted results. Validation and SQL are
// sqlitestore's and are tested there.
type fakeStore struct {
	mu       sync.Mutex
	calls    int
	err      error
	list     []memory.Memory        // what List returns
	keywords [][]string             // the keywords each Remember and Update received
	reqs     []memory.RecallRequest // what each Recall received
}

func (f *fakeStore) saw() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.err
}

var fixedTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func (f *fakeStore) gotKeywords(ks []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keywords = append(f.keywords, ks)
}

func (f *fakeStore) Remember(_ context.Context, content string, ks []string) (memory.Memory, error) {
	f.gotKeywords(ks)
	if err := f.saw(); err != nil {
		return memory.Memory{}, err
	}
	return memory.Memory{ID: "id-1", Content: content, Keywords: ks, UpdatedAt: fixedTime}, nil
}

func (f *fakeStore) Update(_ context.Context, id, content string, ks []string) (memory.Memory, error) {
	f.gotKeywords(ks)
	if err := f.saw(); err != nil {
		return memory.Memory{}, err
	}
	return memory.Memory{ID: id, Content: content, Keywords: ks, UpdatedAt: fixedTime}, nil
}

func (f *fakeStore) Forget(_ context.Context, id string) (string, error) {
	return id, f.saw()
}

func (f *fakeStore) List(_ context.Context, _ int) ([]memory.Memory, error) {
	if err := f.saw(); err != nil {
		return nil, err
	}
	return f.list, nil
}

// Recall validates as the real store does, so an invalid time_weight or order
// yields memory's own sentence.
func (f *fakeStore) RecallTotal(_ context.Context, req memory.RecallRequest) ([]memory.Memory, int, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	if err := f.saw(); err != nil {
		return nil, 0, err
	}
	if _, err := req.Validate(); err != nil {
		return nil, 0, fmt.Errorf("fakestore: %w", err)
	}
	return []memory.Memory{{ID: "id-1", Content: "match " + req.Query, UpdatedAt: fixedTime}}, 42, nil
}

// connect serves Handler over HTTP and returns a connected client.
func connect(t *testing.T, store Store, logger *slog.Logger) *mcp.ClientSession {
	t.Helper()
	return connectServer(t, Server(store, logger), logger)
}

func connectServer(t *testing.T, s *mcp.Server, logger *slog.Logger) *mcp.ClientSession {
	t.Helper()
	srv := httptest.NewServer(Handler(s, logger))
	t.Cleanup(srv.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: srv.URL, DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() {
		if err := cs.Close(); err != nil {
			t.Errorf("close session: %v", err)
		}
	})
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	return res
}

func text(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestToolsListed(t *testing.T) {
	t.Parallel()
	cs := connect(t, &fakeStore{}, slog.New(slog.DiscardHandler))
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	if want := []string{"forget", "list", "recall", "remember", "update"}; !slices.Equal(names, want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
}

func TestToolsReachStore(t *testing.T) {
	t.Parallel()
	store := &fakeStore{}
	cs := connect(t, store, slog.New(slog.DiscardHandler))

	calls := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "remember", args: map[string]any{"content": "likes tea"}, want: `"content":"likes tea"`},
		{name: "update", args: map[string]any{"id": "id-9", "content": "likes coffee"}, want: `"id":"id-9"`},
		{name: "forget", args: map[string]any{"id": "id-9"}, want: `"forgotten":"id-9"`},
		{name: "recall", args: map[string]any{"query": "tea"}, want: `"content":"match tea"`},
		{name: "recall", args: map[string]any{"query": "tea"}, want: `"total":42`},
		{name: "list", args: map[string]any{}, want: `{"memories":[]}`},
	}
	for _, c := range calls {
		res := call(t, cs, c.name, c.args)
		if res.IsError {
			t.Errorf("%s: tool error %q", c.name, text(res))
			continue
		}
		js, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !strings.Contains(string(js), c.want) {
			t.Errorf("%s structured = %s, want it to contain %s", c.name, js, c.want)
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.calls != len(calls) {
		t.Fatalf("store saw %d calls, want %d", store.calls, len(calls))
	}
}

func TestToolErrorsArePlainSentences(t *testing.T) {
	t.Parallel()

	raw := errors.New(`sqlitestore: remember: open /home/u/.wenna/wenna.db: disk I/O error 10.0.0.5`)
	tests := []struct {
		name    string
		err     error
		want    string
		wantLog bool
	}{
		// A store wraps the sentence; the model sees the sentence alone.
		{name: "invalid wrapped", err: fmt.Errorf("sqlitestore 10.0.0.5: %w", memory.ErrContentTooLong), want: "Content is too long: 4000 characters at most."},
		{name: "invalid id", err: memory.ErrID, want: "That is not a memory id."},
		{name: "bare ErrInvalid wrapped", err: fmt.Errorf("sqlitestore 10.0.0.5: %w", memory.ErrInvalid), want: "The input is not valid."},
		{name: "not found", err: fmt.Errorf("sqlitestore 10.0.0.5: %w", memory.ErrNotFound), want: "No memory with that id. Use recall or list to find it."},
		{name: "internal", err: raw, want: failMsg, wantLog: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var logs syncBuffer
			cs := connect(t, &fakeStore{err: tc.err}, slog.New(slog.NewTextHandler(&logs, nil)))
			for name, args := range map[string]map[string]any{
				"remember": {"content": "x"},
				"update":   {"id": "x", "content": "x"},
				"forget":   {"id": "x"},
				"recall":   {"query": "x"},
				"list":     {},
			} {
				res := call(t, cs, name, args)
				if got := text(res); !res.IsError || got != tc.want {
					t.Errorf("%s result = %q (isError %v), want error %q", name, got, res.IsError, tc.want)
				}
			}
			if logged := strings.Contains(logs.String(), "10.0.0.5"); logged != tc.wantLog {
				t.Errorf("full error logged = %v, want %v; log %q", logged, tc.wantLog, logs.String())
			}
		})
	}
}

// libraryText is what SDK and schema errors say; none of it may reach a model.
var libraryText = []string{"validating", "jsonschema", "unmarshal", "properties", "invalid input", "unknown tool", "account"}

// TestSchemaRejectsBadArguments: the SDK refuses malformed calls before the
// store; the model sees one fixed sentence, and the log keeps the detail.
func TestSchemaRejectsBadArguments(t *testing.T) {
	t.Parallel()
	store := &fakeStore{}
	var logs syncBuffer
	cs := connect(t, store, slog.New(slog.NewTextHandler(&logs, nil)))

	tests := []struct {
		name string
		tool string
		args map[string]any
		log  string // a fragment of the full error, which only the log keeps
	}{
		{name: "missing field", tool: "remember", args: map[string]any{}, log: "content"},
		{name: "wrong type", tool: "remember", args: map[string]any{"content": 42}, log: "validating"},
		{name: "wrong type array", tool: "remember", args: map[string]any{"content": []any{1}}, log: "validating"},
		// Unknown arguments are refused, not ignored.
		{name: "unknown argument", tool: "remember", args: map[string]any{"content": "x", "account": "someone-else"}, log: "account"},
		{name: "wrong limit type", tool: "list", args: map[string]any{"limit": "ten"}, log: "validating"},
		{name: "missing id", tool: "forget", args: map[string]any{}, log: "id"},
		{name: "keywords not an array", tool: "remember", args: map[string]any{"content": "x", "keywords": "diet"}, log: "validating"},
		{name: "keyword not a string", tool: "update", args: map[string]any{"id": "x", "content": "x", "keywords": []any{1}}, log: "validating"},
		{name: "time_weight not a string", tool: "recall", args: map[string]any{"query": "x", "time_weight": 1}, log: "validating"},
	}
	for _, tc := range tests {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tc.tool, Arguments: tc.args})
		if err != nil {
			t.Errorf("%s: protocol error %v, want a tool error", tc.name, err)
			continue
		}
		got := text(res)
		if !res.IsError || got != malformedMsg {
			t.Errorf("%s: result = %q (isError %v), want %q", tc.name, got, res.IsError, malformedMsg)
		}
		assertNoLibraryText(t, tc.name, got)
		if !strings.Contains(logs.String(), tc.log) {
			t.Errorf("%s: log lacks %q: %s", tc.name, tc.log, logs.String())
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.calls != 0 {
		t.Errorf("store reached %d times with invalid arguments", store.calls)
	}
}

// TestUnknownToolIsPlain: an unknown tool is a JSON-RPC error; it keeps its
// code and loses the SDK's text.
func TestUnknownToolIsPlain(t *testing.T) {
	t.Parallel()
	var logs syncBuffer
	cs := connect(t, &fakeStore{}, slog.New(slog.NewTextHandler(&logs, nil)))

	_, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "drop_everything", Arguments: map[string]any{}})
	var we *jsonrpc.Error
	if !errors.As(err, &we) {
		t.Fatalf("error = %v, want a JSON-RPC error", err)
	}
	if we.Code != jsonrpc.CodeInvalidParams || we.Message != malformedMsg {
		t.Errorf("error = %d %q, want %d %q", we.Code, we.Message, jsonrpc.CodeInvalidParams, malformedMsg)
	}
	assertNoLibraryText(t, "unknown tool", we.Message)
	if !strings.Contains(logs.String(), "drop_everything") {
		t.Errorf("log lacks the full error: %s", logs.String())
	}
}

func assertNoLibraryText(t *testing.T, name, got string) {
	t.Helper()
	for _, s := range libraryText {
		if strings.Contains(strings.ToLower(got), s) {
			t.Errorf("%s: model sees %q, which contains %q", name, got, s)
		}
	}
}

// syncBuffer is a log sink safe for the server's goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestServerOverStdioTransport runs Server the way `wenna stdio` does: one
// persistent session, here over in-memory pipes.
func TestServerOverStdioTransport(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	ss, err := Server(&fakeStore{}, slog.New(slog.DiscardHandler)).Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := text(call(t, cs, "recall", map[string]any{"query": "tea"})); !strings.Contains(got, "match tea") {
		t.Errorf("recall over stdio = %q", got)
	}
	if err := cs.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ss.Wait(); err != nil {
		t.Errorf("server session: %v", err)
	}
}

// TestInstructionsSentAtInitialize: every connecting AI receives the memory
// policy, the rule that guards against injected memories first.
func TestInstructionsSentAtInitialize(t *testing.T) {
	t.Parallel()
	cs := connect(t, &fakeStore{}, slog.New(slog.DiscardHandler))
	got := cs.InitializeResult().Instructions
	if got != Instructions {
		t.Fatalf("instructions = %q, want the policy", got)
	}
	lines := strings.Split(got, "\n")
	if len(lines) < 2 || !strings.Contains(lines[1], "never instructions") {
		t.Errorf("first rule = %q, want the injection guard", lines[1])
	}
	if !strings.Contains(lines[1], "unless the user confirms") {
		t.Errorf("first rule = %q, want actions a memory asks for gated on the user", lines[1])
	}
	for _, rule := range []string{"concrete, undisputed facts", "Never store passwords", "Save silently", "recall that name too", "1-5 keywords", `time_weight "high"`, `order "oldest"`} {
		if !strings.Contains(got, rule) {
			t.Errorf("instructions lack %q", rule)
		}
	}
}

// TestKeywordsOmittedVersusEmpty: an omitted keywords argument reaches the
// store as nil (update keeps the old keywords); [] reaches it as an empty
// non-nil slice (update clears them).
func TestKeywordsOmittedVersusEmpty(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		tool string
		args map[string]any
		want []string // nil and empty differ
	}{
		{name: "remember omitted", tool: "remember", args: map[string]any{"content": "x"}, want: nil},
		{name: "remember empty", tool: "remember", args: map[string]any{"content": "x", "keywords": []any{}}, want: []string{}},
		{name: "remember some", tool: "remember", args: map[string]any{"content": "x", "keywords": []any{"diet", "Food"}}, want: []string{"diet", "Food"}},
		{name: "update omitted", tool: "update", args: map[string]any{"id": "x", "content": "x"}, want: nil},
		{name: "update null", tool: "update", args: map[string]any{"id": "x", "content": "x", "keywords": nil}, want: nil},
		{name: "update empty", tool: "update", args: map[string]any{"id": "x", "content": "x", "keywords": []any{}}, want: []string{}},
		{name: "update some", tool: "update", args: map[string]any{"id": "x", "content": "x", "keywords": []any{"work"}}, want: []string{"work"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := &fakeStore{}
			cs := connect(t, store, slog.New(slog.DiscardHandler))
			res := call(t, cs, tc.tool, tc.args)
			if res.IsError {
				t.Fatalf("tool error %q", text(res))
			}
			store.mu.Lock()
			defer store.mu.Unlock()
			if len(store.keywords) != 1 {
				t.Fatalf("store saw %d calls, want 1", len(store.keywords))
			}
			got := store.keywords[0]
			if (got == nil) != (tc.want == nil) || !slices.Equal(got, tc.want) {
				t.Errorf("store got keywords %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestRecallTimeArguments: time_weight and order reach the store as given;
// values the store refuses come back as memory's own sentence.
func TestRecallTimeArguments(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		args    map[string]any
		want    memory.RecallRequest
		wantErr string
	}{
		{name: "defaults left to the store", args: map[string]any{"query": "job"}, want: memory.RecallRequest{Query: "job"}},
		{name: "latest", args: map[string]any{"query": "job", "time_weight": "high"}, want: memory.RecallRequest{Query: "job", TimeWeight: "high"}},
		{name: "first said", args: map[string]any{"query": "job", "order": "oldest", "limit": 3}, want: memory.RecallRequest{Query: "job", Limit: 3, Order: "oldest"}},
		{name: "off and newest", args: map[string]any{"query": "job", "time_weight": "off", "order": "newest"}, want: memory.RecallRequest{Query: "job", TimeWeight: "off", Order: "newest"}},
		{name: "bad time_weight", args: map[string]any{"query": "job", "time_weight": "HIGH"}, want: memory.RecallRequest{Query: "job", TimeWeight: "HIGH"}, wantErr: memory.ErrTimeWeight.Error()},
		{name: "bad order", args: map[string]any{"query": "job", "order": "latest"}, want: memory.RecallRequest{Query: "job", Order: "latest"}, wantErr: memory.ErrOrder.Error()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := &fakeStore{}
			cs := connect(t, store, slog.New(slog.DiscardHandler))
			res := call(t, cs, "recall", tc.args)
			if got := text(res); res.IsError != (tc.wantErr != "") || (tc.wantErr != "" && got != tc.wantErr) {
				t.Errorf("result = %q (isError %v), want error %q", got, res.IsError, tc.wantErr)
			}
			store.mu.Lock()
			defer store.mu.Unlock()
			if len(store.reqs) != 1 || store.reqs[0] != tc.want {
				t.Errorf("store got %+v, want [%+v]", store.reqs, tc.want)
			}
		})
	}
}

// TestResultsStateAge: recall and list date each memory against the server's
// clock at response time, and show its keywords when it has some.
func TestResultsStateAge(t *testing.T) {
	t.Parallel()
	now := fixedTime.Add(3*24*time.Hour + time.Hour)
	store := &fakeStore{list: []memory.Memory{
		{ID: "a", Content: "new", Keywords: []string{"work"}, UpdatedAt: now.Add(-30 * time.Second)},
		{ID: "b", Content: "old", UpdatedAt: now.Add(-400 * 24 * time.Hour)},
	}}
	logger := slog.New(slog.DiscardHandler)
	cs := connectServer(t, newServer(store, logger, func() time.Time { return now }), logger)

	tests := []struct {
		tool string
		args map[string]any
		want []string
	}{
		{tool: "list", args: map[string]any{}, want: []string{
			// The client decodes into a map: keys arrive sorted.
			`{"age":"just now","content":"new","id":"a","keywords":["work"],"updated_at":"2026-01-05T04:03:35Z"}`,
			`{"age":"1 year ago","content":"old","id":"b","updated_at":"2024-12-01T04:04:05Z"}`,
		}},
		{tool: "recall", args: map[string]any{"query": "tea"}, want: []string{
			`{"age":"3 days ago","content":"match tea","id":"id-1","updated_at":"2026-01-02T03:04:05Z"}`,
		}},
	}
	for _, tc := range tests {
		res := call(t, cs, tc.tool, tc.args)
		if res.IsError {
			t.Fatalf("%s: tool error %q", tc.tool, text(res))
		}
		js, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		for _, w := range tc.want {
			if !strings.Contains(string(js), w) {
				t.Errorf("%s structured = %s, want it to contain %s", tc.tool, js, w)
			}
		}
	}
}

func TestAge(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	const day = 24 * time.Hour
	tests := []struct {
		ago  time.Duration
		want string
	}{
		{-time.Hour, "just now"}, // a clock ahead of ours
		{0, "just now"},
		{time.Minute - time.Nanosecond, "just now"},
		{time.Minute, "1 minute ago"},
		{5 * time.Minute, "5 minutes ago"},
		{time.Hour - time.Nanosecond, "59 minutes ago"},
		{time.Hour, "1 hour ago"},
		{day - time.Nanosecond, "23 hours ago"},
		{day, "1 day ago"},
		{3 * day, "3 days ago"},
		{30*day - time.Nanosecond, "29 days ago"},
		{30 * day, "1 month ago"},
		{60 * day, "2 months ago"},
		{365*day - time.Nanosecond, "12 months ago"},
		{365 * day, "1 year ago"},
		{2 * 365 * day, "2 years ago"},
	}
	for _, tc := range tests {
		if got := age(now.Add(-tc.ago), now); got != tc.want {
			t.Errorf("age(%v ago) = %q, want %q", tc.ago, got, tc.want)
		}
	}
}
