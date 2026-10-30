# Go tooling — evidence behind the rules

> **WRONG FILE?** Go (`backend/`) → this file. Harness rules (`.env`,
> Makefiles) → CLAUDE.md § Working in this environment.

[`go.md`](go.md) holds the one-line rule for everything here. If the two
disagree, `go.md` is the rule and this file is the evidence — tell the human,
because they should not disagree.

The ratios below are properties of the tools; re-measure here before quoting a
number as this repo's.

## When to read this file

- Before your first `Read` of an unfamiliar package.
- Before any multi-site edit.
- Before writing a mutation harness.
- When a semantic question ("who implements this?", "is this dead?") is about to
  be answered with `grep`.

---

## Read signatures first

**Start every unfamiliar package with `go doc`, not `Read`.** `go doc -short`
renders the exported API in a fraction of the source's size, and it is the half
that answers "what does this package offer?"

```
go doc -short ./<pkg>          # every exported symbol, signatures only
go doc ./<pkg>                 # + package doc and type summaries
go doc -u -short ./<pkg>       # include UNEXPORTED — usually what you need in-package
go doc ./<pkg> Symbol          # one symbol, full doc comment
go doc -u -src ./<pkg> Symbol  # ONE SYMBOL'S EXACT SOURCE
go doc -u -src ./<pkg> T.M     # same, for a method
go list ./...                  # the package inventory
```

**`-src` pays twice.** It prints the verbatim source of one symbol without
opening the file — measured 25× smaller than the file holding it. The bigger
payoff is edit quality: `Edit` needs `old_string` to match exactly, and the usual
failure is an anchor reconstructed from memory. `-src` hands you the **real
bytes**. Anchor on the artifact, not on your recollection of it.

- **`-u` is required for unexported symbols.** `go doc -src <pkg> lowerName`
  fails; `go doc -u -src` works.
- **`Type.Method` works.**
- **A package-level `-src` does not dump every body** — always pass a symbol.
- **`-src` is symbol-granular.** A mid-function anchor is not reachable by it;
  `Read` with `offset`/`limit` is correct there, not a fallback.

**Reading order that keeps context small:**

1. `go doc -u -short` → the shape.
2. `go doc <pkg> <Symbol>` on the two or three that matter → the contract, in
   the author's words.
3. `go doc -u -src <pkg> <Symbol>` on what you are about to change → exact
   source and Edit anchor.
4. `gopls symbols <file>` → a file's contents without reading it.
5. `gopls references` / `call_hierarchy` → who uses it.
6. **Only then** `Read`, only the range you need.

**Never `cat` a package to find out what is in it.**

**`go doc` needs the package to type-check.** With agents mid-edit a package may
transiently fail — a signal about the tree, not your approach. Fall back to
`gopls symbols` (syntax-level) or wait.

### Where signature-first is the WRONG tool

- **When the deliverable is prose.** Code that reads like its neighbours means
  imitating their doc comments, and `go doc -short` strips exactly that. Read the
  neighbours in full.
- **When the answer is inside a function body.** `go doc` renders signatures.
  For a config layer, where every env name is a literal inside `Load`, it
  answers none of the question — though it still shows the shape.
- **When you need byte-exact anchors** across a file (mutation testing).

`go doc` answers "what shape is this package". For "why", "what does this do",
or "what exact bytes are here", `Read` is right.

### Two things that beat every tool here

- **The compiler is a better `references` for a signature change.** Widen an
  interface, run `go vet ./...`: it names every implementation that broke, with
  `file:line`, as ground truth rather than an index. `references` still wins for
  *"is this dead?"*, where nothing breaks.
- **A 20-line scratch program settles behaviour questions** reading cannot:
  do `encoding/json` / `fmt` / `slog` recurse into nested types
  for `MarshalJSON` / `Stringer` / `LogValuer` (**json and fmt yes, slog's
  `LogValuer` no**). Documentation describes; only the runtime is.

## Use the language server, not grep, for questions about code

**Use the language server.** Whether a session has the `LSP` tool depends on
the runtime and how an agent was dispatched — check with `ToolSearch`, don't
assume. The `gopls` CLI answers the same questions everywhere it is installed;
its commands are in [go.md](go.md) § Reading code.

| Question | gopls | Why grep is worse |
|---|---|---|
| Who implements this interface? | `implementation` | Go has no `implements` keyword — there is no string to grep |
| Who calls this? Is it dead? | `references`, `call_hierarchy` | Catches method values, interface dispatch, aliases |
| Where is this defined? | `definition` | Survives shadowing and same-named symbols |
| What is in this file? | `symbols` | Cheaper than reading it |

Substitutes that beat both:

- *"Who breaks if I change this signature?"* → change it and run `go vet ./...`.
- *"Now rewrite every call site"* → `gofmt -r 'New(a,b) -> New(a,noSeed,b)'`,
  previewed as a diff. **Compiler to find, `gofmt -r` to rewrite** — the
  strongest refactoring loop available.
- *"Is this dead?"* with only grep → say in your report the claim rests on grep;
  it cannot see interface dispatch or a value passed through a constructor.

**Where grep is still right:** non-Go files, comments and prose, build tags,
shell, config, and *"does this literal appear anywhere"*.

**A grep COUNT is a candidate list, never a finding.** It counts comment
mentions and misses spelling variants. If a count is load-bearing, **get it from
something that executes.**

**There is no semantic search for shell.** Grep once for "who calls this
script", then pin the result with a self-test assertion.

**An `Edit` that "succeeds" is not proof the change landed where you meant.**
Grep for a unique token afterwards when the anchor was uncertain.

## Editing at scale: type-aware tools, never `sed`

- **`gopls rename`** — type-aware, workspace-wide; follows interface
  satisfaction, embedded fields and shadowing, and **refuses** a rename that
  would break compilation.

  ```
  gopls rename -l <file>:<line>:<col> NewName   # files it would touch
  gopls rename -d <file>:<line>:<col> NewName   # unified diff — REVIEW THIS
  gopls rename -w <file>:<line>:<col> NewName   # write
  ```

  Bare (no flag) it prints whole edited files to stdout — always pass `-l` or
  `-d`. The workspace must build for the types to be right.

- **`gofmt -r 'pattern -> replacement'`** — AST-aware expression rewrites.
  Single lowercase identifiers are wildcards. **Expression-only:** a statement
  pattern fails to *parse* (`expected operand, found 'if'`).

- **`ast-grep`** — structural search/rewrite across statements. Three caveats:
  1. **It over-matches in ways that look precise.** `if $E != nil { return nil }`
     also matched `return nil, fmt.Errorf("…: %w", err)` — correct code —
     because `return nil` matches a return list that merely *begins* with `nil`.
     **An ast-grep count is a candidate list.**
  2. **Go patterns need a complete statement context.** `os.Getenv($A)` matches
     nothing; `$X := os.Getenv($A)` works. A zero is ambiguous between "the
     invariant holds" and "my pattern is wrong" — **validate every pattern
     against a known true positive before trusting a zero.**
  3. **It is syntax-aware, not type-aware.** Types belong to gopls.

**None of these is `sed`.** A `sed` sweep cannot tell a call from a comment,
cannot see a shadowed name, and rewrites an unrelated package that shares an
identifier.

**The one exception: bytes INSIDE a string literal** are one token to every AST
tool, so a byte-level rewrite is correct there. Two traps:

- **perl's UTF-8 flags fail silently, in both directions.** Measured on
  `café`: bare `perl -i -pe` handles a literal `é` but not `\x{e9}`; `-CSD`
  alone handles `\x{e9}` but not a literal `é`. Each miss is exit 0, no diff.
  `-CSD -Mutf8` handles both. Verify the edit landed.
- **Write escapes for bidi and zero-width characters from the start.**
  Adversarial tests contain them naturally, and linters (staticcheck ST1018,
  if adopted) reject them raw.

**`Edit` converts `\uXXXX` escapes in `new_string` into literal characters.**
Write control codepoints with a tool that keeps the escape.

**Pick by the question:** semantic (rename, references, implementations) →
gopls; statement-level structure → ast-grep, verifying matches; expression
rewrite → `gofmt -r`; strings, comments, build tags, non-Go → grep. A behavioural
edit at each site → `gopls references` for the true list, then one `Edit` per
site.
