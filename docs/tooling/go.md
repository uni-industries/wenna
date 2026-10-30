# Go rules

Conventions and tooling rules for all Go code in `backend/`. Companion:
[go-evidence.md](go-evidence.md) carries the measurements and evidence behind
the reading/editing rules.

## Conventions

- **Services are thin** — wiring, transport, lifecycle. Business logic lives in
  importable packages; business logic in a `main` package is a smell.
- **`context.Context` threads through every hop**, first parameter, never stored
  in a struct. Dropping it at a boundary breaks cancellation and telemetry
  silently.
- **Colocated table-driven tests.** Security-critical packages additionally get
  adversarial cases in the same change — the malicious input is the test.
- **Every test is parallel by default.** `t.Parallel()` is the first statement
  of a test, and of each `t.Run` closure that can take it. A test that cannot be
  parallel says why **in its doc comment**, starting with `NOT PARALLEL:`, and
  the reason is a property of the test — it mutates process-global state
  (`t.Setenv`, `t.Chdir`), or reads a process-global measurement
  (`runtime.MemStats`) — never *"not done yet"*.
  Parallelism must not change what a test proves: shared fixtures, temp dirs,
  ports and golden files get isolated, never the observation dropped. An
  ephemeral port is unique per **address**, not per host — a test that finds
  "its" socket by port alone finds a neighbour's.
- **No real timers in tests.** No `time.Sleep`/`time.After`/`time.NewTimer`/
  `time.Tick` in a `_test.go`, and **no assertion on elapsed wall-clock time** —
  that measures how busy the machine is, and goes red on a loaded linux runner
  while a laptop stays green. **Never delete a test to satisfy this**; the
  defect is the unmocked clock. Convert it: assert a deterministic observable
  (the flag saying the cap fired, the count of the operation, bytes read) —
  strictly better evidence — otherwise inject a clock and assert against mocked
  time. If neither can express the property, escalate rather than weaken the
  test. Where real elapsed time is unavoidable (a real database), poll a condition
  against a deadline — never sleep a fixed interval.
  Carve-out: a deadline guard that turns a hang into a failure is not a sleep;
  `case <-time.After(…)` inside a `select` is that form.
  **The mirror is the half that gets missed:** a short deadline bounding **real
  I/O** is a race, not a timeout. Where a test drives real I/O, the injected
  clock makes the *wait* free and the budget on the *work* goes UP. A deadline
  the code must **exceed to fail** is a guard — keep it. A deadline the code must
  **come in under to pass** is a wall-clock assertion, whatever it is named. Ask
  which direction failure lies in.
- **Interfaces at the consumer**, not exported from the implementation.
- **No global mutable state you own** — no package-level clients, config, or
  loggers; dependencies are constructor parameters. Process/OS state (`umask`,
  cwd, signals, env) is judged on concurrency and blast radius, not this rule.
- **Config is one validated struct per binary**, parsed once at startup. Nothing
  else reads `os.Getenv`. Missing/malformed values fail at boot, never at first
  use. Never branch on "is this prod" — add a named, validated value. Unset
  either fails validation or means the safe behaviour (loopback only, TLS
  proxy off) — never one environment's answer (CLAUDE.md § Environments).
- **Errors: wrap with `%w` and context.** Sentinels for conditions callers
  branch on; typed errors when the caller needs detail. Never string-match an
  error message at a call site.
- **Dependencies:** `go get`, one at a time, and say why. A new dependency is a
  decision for the human.
- **cgo:** the SQLite driver needs it (§ Decisions, memory store). Nothing
  else may; another use is a decision.

## Building

- **Build, test and vet through the Makefile** (`make build`, `make ci`). It
  owns the build tags — `sqlite_fts5` among them, without which the binary
  compiles and full-text search fails at run time — and exports them in
  `GOFLAGS`, so the binaries tests build with `go build` get them too.
- **Running `go` directly** (one test, gopls, an editor) needs the Makefile's
  `TAGS` in the environment, comma-joined: `GOFLAGS=-tags=sqlite_fts5`. For
  gopls, set it in the editor's environment. A `-tags` flag on the command
  line does not reach the `go build` a test runs; `GOFLAGS` does.

## Logging

- **Every binary that logs installs `slog.NewTextHandler` on stderr — logfmt.**
  No format switch: a value no environment sets differently is a branch wearing
  configuration's hat. Revisit only if something parses log fields.
- **Wrap errors before logging them; the text handler renders an error with
  `%+v`.** An error carrying a `Format` method (`github.com/pkg/errors` and
  similar) prints its stack trace and absolute source paths. An
  `fmt.Errorf`-wrapped chain has no `Format` method and is unaffected.
- **`[]byte` and `[]string` attributes render poorly under the text handler** —
  `[]string` is space-joined with no per-element quoting. Pass a joined string
  you control, or a count.
- **Redact on the outermost struct that is ever logged.** `slog` does not
  recurse looking for a `LogValuer`, and a whole config is how a secret reaches
  a log.
- **A credential-redaction test drives the real handlers and every verb** —
  `%v`, `%+v`, `%#v`, `%s`, `%q`, `json.Marshal`, and **both** `slog` handlers.
  `%#v` bypasses `String()`; `encoding/json` reads exported fields only while
  the text handler's `%+v` reaches unexported ones. A type defended only by
  `MarshalJSON` leaks under text, and one defended only by `String` leaks under
  JSON.
- **Pair every absence assertion with a vacuity control** — the same renderers
  over an *undefended* struct carrying the same canary, required to leak. An
  all-unexported struct renders as `{}` under JSON, so "the secret is absent"
  can pass while nothing was rendered.
- **Assert interface satisfaction at compile time** — `var _ slog.LogValuer = T{}`
  — for every interface whose purpose is to be called implicitly.
  `LogValue() any` compiles, reads correctly, and is never called.

## Reading code

- **Start with `go doc`, never `Read`/`cat`, to learn a package.**
  `go doc -u -short <pkg>` for the shape (`-u` — most security-critical code is
  unexported); `go doc -u -src <pkg> <Symbol>` for one symbol's exact source and
  a byte-exact Edit anchor.
- `go doc` is wrong when the deliverable is prose, the answer is inside a
  function body, or you need a mid-function anchor — use `Read` there.
- **Semantic questions go to the language server** — the `LSP` tool where your
  session has it (check with `ToolSearch`), otherwise the `gopls` CLI:
  `gopls implementation|references|definition|symbols|call_hierarchy <file>:<line>:<col>`,
  1-based on the identifier. `implementation` has no grep substitute. `references` is how you establish
  "nothing constructs this in production". A missing caller means your reading,
  build tags, or a stale index.
- **`grep -v _test.go` before concluding "test-only", and never pipe through
  `head`** — the one production caller sits mid-list.
- When the question is behaviour, write a 20-line scratch program and ask the
  runtime. For a pinned dependency, read the module cache source, not the
  vendor's docs.

## Editing at scale — never `sed`

- "Who breaks if I change this signature?" → change it and run `go vet ./...`;
  it names every call site.
- "Rewrite them all" → `gofmt -r 'pattern -> replacement'`, previewed as a
  diff. `gofmt -r` is expression-only; `ast-grep` reaches statements but
  over-matches. `gopls rename -d` for a type-aware rename.
- Bytes inside string literals are invisible to AST tools — the one place a
  byte rewrite is right. Use `perl -CSD -Mutf8 -i -pe`, then verify the edit
  landed — any other flag set silently skips one of literal or `\x{…}`
  patterns, exit 0.
- The `Edit` tool converts `\uXXXX` escapes in `new_string` to literal
  characters — this puts real NUL bytes in source files.
