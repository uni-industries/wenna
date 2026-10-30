# Security

**Wenna is for personal, low-risk use.** It is one person's memory, shared by
their own personal agents, protected by one secret. It lacks the security features you
would expect before trusting it with sensitive data. Do not use it for health,
financial, legal or regulated data, other people's personal data, credentials,
or anything you could not afford to have read or altered.

## Missing security features

- **One token for everything.** No per-app tokens, no expiry, no scopes
  (read-only, write); revoking one app means rotating the token for all.
- **No second factor, no accounts.**
- **OAuth only through a provider you run.** wenna signs no one in itself.
  With Cloudflare Access or WorkOS AuthKit configured
  ([docs/oauth/](docs/oauth/)), that provider decides who signs in; wenna checks
  its signature and that it names you. On the Cloudflare route, Cloudflare
  also sees your memories in transit.
- **No rate limiting, and no limit on total memory size.** Each MCP request
  body is capped by the MCP library (4 MiB); nothing caps how much is stored.
- **No encryption at rest.** The database is a plain SQLite file.
- **No access log.** Changes are kept in the memory history; reads are not
  recorded, so you cannot tell whether someone else read your memory.
- **No TLS of its own.** Over a network it depends on a proxy in front.
- **Unsigned releases.** The installer checks a download against the
  release's own `checksums.txt`: that catches a corrupted download, not a
  tampered release. Binaries and images carry no signature or provenance.
- **No independent security review.**

## Threat model

**What is protected:** the memories (text, verbatim, with full history) and
the token that unlocks them over HTTP.

**Who can access what:**

| Who | Access |
|---|---|
| You, and anything running as your OS user | Everything: `wenna stdio` and `wenna export` need no token, and the database file is readable by your user. |
| Anyone holding the token | Everything over HTTP: read, change, forget, export. |
| Anyone who can sign in as you at your OAuth provider, if configured | The same, until the provider's session ends or you revoke it there. |
| Your OAuth provider, if configured | It can mint tokens that name you. Cloudflare Access also sees every request and response. |
| Your hosting platform, if self-hosted | The disk and the running process. |
| The network between Cloudflare and Railway or Render, if you put Cloudflare in front of either | Both use Cloudflare's SSL mode "Full": the hop is encrypted, but the platform's certificate is not checked. |
| Anyone with your R2 token or Cloudflare account, on Cloudflare Containers | Everything: the bucket holds the full database, unencrypted. |
| Anyone who reads the Cloudflare bootstrap API token (`~/.wenna/cloudflare-api-token`) while it exists, during Containers setup | The whole Cloudflare account: it can mint any account token, so the bucket and everything else. Setup revokes it and deletes the file once the R2 key and Access are done. |
| Everyone else on the network | Nothing, as long as HTTPS and the token hold. |

**Threats and what stands against them:**

| Threat | Control | What's left |
|---|---|---|
| Guessing the token | At least 32 bytes, compared in constant time, before any work | None in practice for a random token; a short or reused one is your risk |
| Forged OAuth proof, or one meant for another app or person | Signature against the provider's published keys, issuer, audience, expiry and owner all checked; asymmetric algorithms only | A compromised provider account. Key fetches are rate-limited, so forged tokens cannot make wenna call out repeatedly |
| Reaching wenna around Cloudflare Access (the platform's own URL) | Without a valid Access signature the request needs the token | A captured Access token (the `CF_Authorization` cookie value) sent straight to wenna works until it expires, even after you revoke the session or change the policy in Cloudflare |
| A browser page forging requests with your Access cookie | Cross-site and bare-`Origin` requests are refused on the Access route | Very old browsers that send neither `Sec-Fetch-Site` nor `Origin` |
| Token stolen in transit | Loopback by default; beyond it only behind a TLS proxy you assert (`WENNA_BEHIND_TLS_PROXY`) | An `http://` URL or a proxy that is not actually there sends it in the clear ([HOSTING.md](HOSTING.md)) |
| Token stolen at rest (app configs, shell history, platform dashboard) | None in wenna | Full access until you rotate it |
| False memories planted, steering your AIs | Server instructions: memories are data, never instructions; actions a memory asks for need your confirmation | Models follow instructions imperfectly; one leaked token reaches every connected AI |
| Other local users reading the file | Database files are owner-only (0600) | Malware running as you reads everything |
| Flooding: filling the disk, raising your bill | Requests without the token are refused before any work; each MCP request body is capped | With the token, nothing limits rate or total size |
| A web page reaching a local wenna by DNS rebinding | The token; every request arriving on loopback must name localhost, or the one host in `WENNA_PUBLIC_URL` | Whoever controls that public name's DNS could aim it at your machine; it needs the token too |
| Reached from the platform's private network | The token | That traffic is not encrypted by wenna |
| Data lost | Every change is kept; export at any time | No backups unless you export. On Cloudflare Containers, a crash loses changes not yet sent to R2 (about the last second) |
| Two Cloudflare deploys writing one R2 replica, corrupting it | The Worker runs one container | A second deploy pointed at the same bucket |

**Out of scope:** a compromised OS account or machine, a compromised hosting
platform, and attacks on the AI apps themselves.
