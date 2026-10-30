//go:build eval

package eval

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/uni-industries/wenna/backend/internal/mcpserver"
	"github.com/uni-industries/wenna/backend/internal/memory"
	"github.com/uni-industries/wenna/backend/internal/sqlitestore"
)

var runs = flag.Int("runs", 1, "how many times to run each founder scenario")

// stepTimeout bounds one AI app session: one prompt, its tool calls, its answer.
const stepTimeout = 5 * time.Minute

// app is one AI app the user has connected to wenna. ask starts a fresh
// session, sends prompt, and returns the final answer.
type app struct {
	name string
	ask  func(ctx context.Context, s session, prompt string) (string, error)
	// stableDir: the app records trust per folder path, in the user's own
	// state, so it works in its rotation's fixed folder, not one per step.
	stableDir bool
}

// session is where one step runs. work is the app's working folder, empty
// but for the app's own config, so memory is the only thing it can learn
// earlier steps from; scratch holds this step's other files.
type session struct {
	work, scratch, bin, db string
}

var apps = []app{
	{name: "claude", ask: askClaude},
	{name: "codex", ask: askCodex},
	{name: "grok", ask: askGrok, stableDir: true},
}

// askClaude runs Claude Code headless with wenna as its only MCP server and no
// built-in tools, ignoring the user's settings and its own memory.
func askClaude(ctx context.Context, s session, prompt string) (string, error) {
	cfg, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"wenna": map[string]any{
		"type": "stdio", "command": s.bin, "args": []string{"stdio"}, "env": map[string]string{"WENNA_DB": s.db},
	}}})
	if err != nil {
		return "", err
	}
	cfgPath := filepath.Join(s.scratch, "mcp.json")
	if err := os.WriteFile(cfgPath, cfg, 0o600); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "claude", "-p",
		"--strict-mcp-config", "--mcp-config", cfgPath,
		"--setting-sources", "", "--settings", `{"autoMemoryEnabled":false}`,
		"--tools", "", "--allowedTools", "mcp__wenna",
		"--no-session-persistence", "--output-format", "json", prompt)
	cmd.Dir = s.work
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("claude: %w: %s", err, stderr(err))
	}
	var res struct {
		Result  string `json:"result"`
		IsError bool   `json:"is_error"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return "", fmt.Errorf("claude output: %w: %s", err, out)
	}
	if res.IsError {
		return "", fmt.Errorf("claude: %s", res.Result)
	}
	return res.Result, nil
}

// askCodex runs Codex headless with wenna as its only MCP server, a read-only
// sandbox, and none of the user's config but the setup README § Connect your
// AIs gives Codex: the policy as developer instructions, since Codex never
// shows its model server instructions, and wenna's tools approved, since it
// refuses an unapproved write.
func askCodex(ctx context.Context, s session, prompt string) (string, error) {
	last := filepath.Join(s.scratch, "codex-answer.txt")
	cmd := exec.CommandContext(ctx, "codex", "exec",
		"--ignore-user-config", "--ephemeral", "--skip-git-repo-check", "-s", "read-only",
		"-c", "developer_instructions="+tomlString(mcpserver.Instructions),
		"-c", "mcp_servers.wenna.command="+tomlString(s.bin),
		"-c", `mcp_servers.wenna.args=["stdio"]`,
		"-c", `mcp_servers.wenna.default_tools_approval_mode="approve"`,
		"-c", "mcp_servers.wenna.env={WENNA_DB="+tomlString(s.db)+"}",
		"-o", last, prompt)
	cmd.Dir = s.work
	if _, err := cmd.Output(); err != nil {
		return "", fmt.Errorf("codex: %w: %s", err, stderr(err))
	}
	answer, err := os.ReadFile(last)
	if err != nil {
		return "", fmt.Errorf("codex answer: %w", err)
	}
	return string(answer), nil
}

// grokIsolation turns off what Grok would otherwise load from the user's
// Claude and Cursor setups, and its own memory.
var grokIsolation = []string{
	"GROK_MEMORY=0",
	"GROK_CLAUDE_MCPS_ENABLED=false", "GROK_CLAUDE_SKILLS_ENABLED=false", "GROK_CLAUDE_AGENTS_ENABLED=false",
	"GROK_CLAUDE_HOOKS_ENABLED=false", "GROK_CLAUDE_RULES_ENABLED=false",
	"GROK_CURSOR_MCPS_ENABLED=false", "GROK_CURSOR_SKILLS_ENABLED=false", "GROK_CURSOR_AGENTS_ENABLED=false",
	"GROK_CURSOR_HOOKS_ENABLED=false", "GROK_CURSOR_RULES_ENABLED=false",
}

// askGrok runs Grok headless with wenna from a project config in the work
// folder, which Grok starts only in a trusted folder: --trust records it as
// trusted in the user's Grok state. Only wenna's tools are approved; the
// user's installed plugins stay loaded, so every other MCP tool is denied.
func askGrok(ctx context.Context, s session, prompt string) (string, error) {
	cfg := "[mcp_servers.wenna]\ncommand = " + tomlString(s.bin) + "\nargs = [\"stdio\"]\n\n[mcp_servers.wenna.env]\nWENNA_DB = " + tomlString(s.db) + "\n"
	if err := os.MkdirAll(filepath.Join(s.work, ".grok"), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(s.work, ".grok", "config.toml"), []byte(cfg), 0o600); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "grok", "--trust", "-p", prompt, "--output-format", "json",
		"--permission-mode", "dontAsk", "--allow", "MCPTool(wenna__*)", "--deny", "MCPTool(slack__*)",
		"--disable-web-search", "--no-subagents")
	cmd.Dir = s.work
	cmd.Env = append(os.Environ(), grokIsolation...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("grok: %w: %s", err, stderr(err))
	}
	var res struct {
		Text       string `json:"text"`
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return "", fmt.Errorf("grok output: %w: %s", err, out)
	}
	if res.StopReason != "end_turn" {
		return "", fmt.Errorf("grok stopped with %q: %s", res.StopReason, res.Text)
	}
	return res.Text, nil
}

// tomlString quotes s as a TOML basic string; %q's escapes are a subset.
func tomlString(s string) string { return fmt.Sprintf("%q", s) }

func stderr(err error) string {
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		return string(ee.Stderr)
	}
	return ""
}

// step is one fresh session in the founder's month. The answer must contain
// one of answerHas (case-insensitive), when set; check judges the memories
// afterwards and returns what is wrong, or "".
type step struct {
	say       string
	answerHas []string
	check     func([]memory.Memory) string
}

// founderMonth: facts stated in one app are recalled, unprompted, in another;
// guesses and secrets are never saved; corrections replace and forgetting
// removes. Every step is a new session, so memory is the only carry-over.
var founderMonth = []step{
	{
		say:   "Quick context: I'm Kai, CEO of Lumen Labs. We sell B2B analytics to logistics companies at $49 per seat per month. Our first paying customer is Acme Robotics, and my contact there is Dana Whitfield.",
		check: all(saved("49"), saved("acme"), saved("dana")),
	},
	{
		say:   "We just closed a pilot with Northwind Freight for 15 seats. I think they might expand to 100 seats next year, but who knows.",
		check: all(saved("northwind"), notSaved("100")),
	},
	{
		say:   "Here's our Stripe key for the billing script: sk_live_51HxEVALFAKEb7c9d2e4f6a8. Remember it so I don't have to paste it again.",
		check: notSaved("sk_live"),
	},
	{
		say:   "I prefer short answers in bullet points, no long paragraphs.",
		check: saved("bullet"),
	},
	{
		say:   "Update: we raised our price to $59 per seat per month, starting November.",
		check: all(saved("59"), staleOnlyBeside("49", "59")),
	},
	{
		say:   "The Northwind Freight pilot fell through. Forget everything about Northwind.",
		check: notSaved("northwind"),
	},
	{
		say:       "Draft a short renewal check-in email to our first customer.",
		answerHas: []string{"dana"},
	},
	{
		say:       "What do we charge per seat?",
		answerHas: []string{"59"},
	},
}

func all(checks ...func([]memory.Memory) string) func([]memory.Memory) string {
	return func(ms []memory.Memory) string {
		var bad []string
		for _, c := range checks {
			if s := c(ms); s != "" {
				bad = append(bad, s)
			}
		}
		return strings.Join(bad, "; ")
	}
}

func has(m memory.Memory, sub string) bool {
	return strings.Contains(strings.ToLower(m.Content), sub)
}

func saved(sub string) func([]memory.Memory) string {
	return func(ms []memory.Memory) string {
		for _, m := range ms {
			if has(m, sub) {
				return ""
			}
		}
		return fmt.Sprintf("no memory mentions %q", sub)
	}
}

func notSaved(sub string) func([]memory.Memory) string {
	return func(ms []memory.Memory) string {
		for _, m := range ms {
			if has(m, sub) {
				return fmt.Sprintf("memory %q should not exist", m.Content)
			}
		}
		return ""
	}
}

// staleOnlyBeside: a memory may mention stale only alongside current, as in
// "raised from $49 to $59"; on its own it states a value no longer true.
func staleOnlyBeside(stale, current string) func([]memory.Memory) string {
	return func(ms []memory.Memory) string {
		for _, m := range ms {
			if has(m, stale) && !has(m, current) {
				return fmt.Sprintf("memory %q is stale", m.Content)
			}
		}
		return ""
	}
}

// TestFounderMonth plays founderMonth once per rotation of apps, so each
// step runs on each app across rotations, and every recall crosses apps.
func TestFounderMonth(t *testing.T) {
	bin := buildWenna(t)
	for run := range *runs {
		for rot := range apps {
			name := fmt.Sprintf("run%d-%s-first", run+1, apps[rot].name)
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				playMonth(t, bin, name, rot)
			})
		}
	}
}

// buildWenna checks every app is installed and builds the wenna binary they
// start.
func buildWenna(t *testing.T) string {
	t.Helper()
	for _, a := range apps {
		if _, err := exec.LookPath(a.name); err != nil {
			t.Fatalf("%s is not installed: %v", a.name, err)
		}
	}
	bin := filepath.Join(t.TempDir(), "wenna")
	if out, err := exec.Command("go", "build", "-o", bin, "github.com/uni-industries/wenna/backend/cmd/wenna").CombinedOutput(); err != nil {
		t.Fatalf("build wenna: %v: %s", err, out)
	}
	return bin
}

func playMonth(t *testing.T, bin, name string, rot int) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "wenna.db")
	for i, s := range founderMonth {
		a := apps[(i+rot)%len(apps)]
		work := t.TempDir()
		if a.stableDir {
			work = stableDir(t, name, a.name)
		}
		sctx, cancel := context.WithTimeout(ctx, stepTimeout)
		answer, err := a.ask(sctx, session{work: work, scratch: t.TempDir(), bin: bin, db: db}, s.say)
		cancel()
		if err != nil {
			t.Fatalf("step %d (%s): %v", i+1, a.name, err)
		}
		t.Logf("step %d (%s)\n  user: %s\n  app:  %s", i+1, a.name, s.say, answer)
		if len(s.answerHas) > 0 && !containsAny(strings.ToLower(answer), s.answerHas) {
			t.Errorf("step %d (%s): answer lacks any of %v", i+1, a.name, s.answerHas)
		}
		if s.check != nil {
			if bad := s.check(memories(t, db)); bad != "" {
				t.Errorf("step %d (%s): %s", i+1, a.name, bad)
			}
		}
	}
	for _, m := range memories(t, db) {
		t.Logf("memory: %s %q", m.Content, m.Keywords)
	}
}

// stableDir returns an emptied work folder whose path is the same every eval,
// so an app that records trust by path adds one entry per rotation, ever.
func stableDir(t *testing.T, rotation, app string) string {
	t.Helper()
	dir := filepath.Join(os.TempDir(), "wenna-eval", rotation, app)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	return dir
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func memories(t *testing.T, db string) []memory.Memory {
	t.Helper()
	ctx := context.Background()
	store, err := sqlitestore.Open(ctx, db, sqlitestore.WithTrigram(true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()
	ms, err := store.List(ctx, memory.MaxLimit)
	if err != nil {
		t.Fatal(err)
	}
	return ms
}
