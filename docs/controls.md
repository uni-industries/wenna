# Shared controls — the one implementation of each

Duplicate implementations are the most expensive defect class in a codebase
like this one, and they present as security bugs: two things reading one input
and disagreeing. **Before implementing anything shared or security-shaped, check
this table, then check the tree.**

Rules:

- **One input, one reader.** If a second reader is unavoidable — two
  components that cannot share code — it must be pinned: a test on each side
  asserting the same literal or the same table, written from the contract, never
  from the other side's source. Pinning each side to the other lets both drift
  onto the same wrong value together.
- **Assert on the artifact at the last point it can still change.** A check that
  runs while something else can still mutate the target is not a check.
- **When you collapse two readers into one type, write the zero-value test in
  the same change**: construct the zero value, run it through the control,
  assert it refuses. A merged type's zero value can be a total bypass that
  neither original had.
- **An absence claim is a hypothesis until you have run `grep`/`ls`/
  `gopls references`.** Building a control a doc wrongly called missing produces
  two implementations of it.
- **If you create a control that belongs here, add the row in the same change.**
  Cite the package, not a file or line.
- **A row may say "nothing implements this yet."** That is a decision on record,
  not a gap to infer from an absence.

| Control | The ONE implementation | Do not write a second because |
|---|---|---|
| Config parsing (backend) | `backend/internal/config` — one validated struct per command, read once at start | Nothing else reads the environment; a second reader is a value that can disagree with the one validated at start (CLAUDE.md § Environments) |
| Who may call the HTTP API | `backend/internal/gate`, before the MCP and export handlers run, admits the owner's proof: `WENNA_TOKEN` as a bearer token (`backend/internal/bearer`, constant time, checked first), or, when configured, an OAuth provider's signed token verified by `backend/internal/authn` — Cloudflare Access's `Cf-Access-Jwt-Assertion` or a WorkOS AuthKit access token — naming the configured owner. Never by hostname (`/api/health` and the protected-resource metadata are open by design and read no data; the metadata answers any origin, `Access-Control-Allow-Origin: *`, the MCP SDK's choice for public discovery data); `backend/internal/config` refuses a non-loopback listen address (no TLS, so the token would travel in the clear) unless `WENNA_BEHIND_TLS_PROXY=true` asserts a TLS-terminating proxy in front; `wenna stdio` has no network surface | A second check is the route that forgets it; one instance is one user, so nothing in a request picks whose data |
| DNS rebinding (a site resolving its name to loopback, through the user's browser) | `backend/internal/rebinding`, mounted by `backend/internal/httpapi` on every protected route (MCP and export), inside `gate`: a request arriving on loopback must name a loopback Host or `WENNA_PUBLIC_URL`'s host (`config.Serve.PublicHost`). Without a public host it refuses exactly what the MCP SDK's own check refuses, which `mcpserver` turns off | A per-route or SDK-side check covers one route and leaves the other open |
| Identifiers | `backend/internal/id` — ULIDs from `crypto/rand`, canonical upper-case text | A second generator or parser disagrees on case, length or alphabet |
| Concurrent writers (one file, many `wenna` processes) | `backend/internal/sqlitestore` — every write is one `BEGIN IMMEDIATE` transaction, so a read-then-insert (dedupe on remember, existence check on update and forget) cannot race another process | A write outside `inTx` is one that resurrects a forgotten memory or saves a duplicate |
| Exporting the history | `backend/internal/export` over `sqlitestore.Export` (one SELECT, one snapshot): `Write` for `wenna export`, `Handler` for `GET /api/export` behind `gate`. The handler builds into an unlinked temp file before any byte is sent, so a crash leaves nothing on disk; one export at a time (503 + Retry-After); a download chunk that stalls 30s is cut off | A second reader of the whole history is a second file format |
| Memory input validation (content, query, id, limit) | `backend/internal/memory`, called by `sqlitestore` on every write and read | A transport-side check is a second reader; the store is the last point before the database |
| Memory schema and migration | `backend/internal/sqlitestore` `schema.sql` then `schema_v2.sql`, applied step by step from `PRAGMA user_version`, each inside a write transaction; a newer version is refused, and so is a build without FTS5 | A second schema source drifts from the one the store queries |
| Recall index (FTS5, names, keywords; trigrams when `WENNA_TRIGRAM=true`) | `backend/internal/sqlitestore` — derived from the event log by one catch-up path, run in every writer's transaction and checked before every recall; the optional trigram index keeps its own position on that path, so processes with the option off leave it behind and the next one with it on catches it up | A second writer of the index is one that misses an event, so recall disagrees with the history |
| CSRF on browser-attached credentials | `backend/internal/gate` `notCrossSite`, on the Cloudflare Access proof: `Sec-Fetch-Site` must be same-origin or none; without it, a bare `Origin` is refused. Bearer tokens are exempt (CLAUDE.md § Security invariants) | One middleware applied to every state-changing route; a per-handler check is the one a new route forgets |
| Input validation | The backend, per route (memory input: row above). A client's validation is a convenience | A client-side check is a second reader that an attacker skips |
