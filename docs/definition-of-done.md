# Definition of done

A change is not done when the code looks right. Before reporting it complete,
all of the following hold, and the report says which checks ran and what they
showed. This file owns *what done means*; commands live in `docs/tooling/`.

## Gates

`make ci` from the repo root. Read
the recipe in the Makefile for what each runs; iterate with the scoped commands
in `docs/tooling/`, but only `make ci` green is ready to merge.

A change to recall, the MCP server instructions or a tool description also
runs `make eval` and reports which steps failed, on which app. It is not a gate:
models vary run to run.

**"Could not run" is as red as "failed."** A check nobody ran and a check that
passed never look alike: write `Blocked: <command>: <why>`.

## Bugs: reproduce first

1. Reproduce the failing path and capture the full output. For a feature, this
   is the failing test.
2. Confirm it is the same failure that was reported — message, assertion, exit
   code. A different error means you are chasing the wrong thing.
3. Fix the root cause.
4. Re-run the identical command; read the full output and the exit code.
5. Keep the reproduction as a regression test.

An unreproduced bug is an unconfirmed bug — say so.

## Tests

- **Write tests from the inputs, not from the coverage report**: every branch,
  boundary, malformed and hostile value. For anything touching auth: no
  credential, an expired one, the wrong owner.
- **Then read coverage of the changed lines** (`go test -coverprofile`, then
  `go tool cover -func`). Aim for 90%+. Below that, add the missing cases or say
  why a line is unreachable by a unit test. Never write a test to raise a number.
- **Red, then green, on the specific defect.** A test you have never seen fail
  is not evidence.
- **A skipped test is not a passing test.** `t.Skip` reports
  green while checking nothing; say why each skip in your area exists.

## Before mutation testing: three checks that find the expensive bugs

1. **Reachability.** For every control the change touches, find its non-test
   callers by references (`gopls`/`LSP`), never `grep -rln`. A middleware that
   exists but is not mounted on the route is not a control.
2. **The seam.** For each pair of controls, ask what sits between them that
   neither checks — the gate and the rebinding guard, the bearer check and the
   Access check.
3. **Two readers.** Wherever two things read one input — a client's
   validation and the backend's, a test stub and the wire format it models —
   assert they agree, or remove the second reader.

## Mutation testing

Mutate each control the change adds, and only those, by hand: edit, run, restore.

- Derive the set from `git diff` plus untracked files, line by line.
- **A guard needs one mutation per clause, varied one at a time.** Deleting all
  of `a || b` hides that nothing exercises `b`.
- **Prove the harness reports both outcomes** before trusting a table.
- **A compile error is not a kill**, and **a hang is not a kill** — score both
  `MUTATION-INVALID`. `go test` exits 1 for both a failing test and a build
  error: a kill requires `--- FAIL: TestName`; `[build failed]` is invalid.
  Read which assertion fired.
- A table is valid only for the tree it ran against; re-run it after any change
  to shared code.
- Mutate in a copy or a worktree; `git diff --stat <path>` between batches.

## Review

- **Adversarial pass by an independent reviewer** for anything touching auth,
  sessions, user data, or secrets — briefed with the code, not
  your conclusion, on every dimension the change touches (concurrency and
  duplicate submits, ordering, config parity across environments).
- **Two rounds by default.** Go past it only when each round's findings are
  unrelated one-offs that converge. If round two is still NO-GO, escalate —
  problem, attempted solution, findings, what you think is actually wrong.
- **Thrashing means the approach is wrong, not the last patch.** When findings
  keep arriving as new instances of one problem, stop patching, name the cause
  that keeps producing them, and propose the redesign that removes the class.
- **No "done" without the human's go-ahead.** Pass remaining concerns verbatim.

## Verify where it can actually break

A single request hides races and double submits. A mocked database hides a query
the real one rejects. Local defaults hide missing config elsewhere. A
clean input hides what validation is for. When tests fake a driver or transport,
at least one run uses the real one. **Check the thing that exists, not the thing
that describes it** — the running app, not the doc; the built binary, not the
source.

## Record it

- **No swallowed issues.** Report what was verified and under which conditions
  ("validated with X under Y; not exercised: Z"). Never "fully tested".
- **A control with only test callers is not done.**
