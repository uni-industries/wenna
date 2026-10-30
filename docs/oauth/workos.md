# OAuth through WorkOS AuthKit: setup runbook

Owns: the steps an agent drives in the user's browser to set up WorkOS AuthKit
as the OAuth server for one wenna instance. The fallback OAuth route
(CLAUDE.md § Decisions); the preferred one is [cloudflare.md](cloudflare.md).

Reference: [WorkOS's MCP docs](https://workos.com/docs/authkit/mcp).
Browser only, except the resource indicator (step 5).

**If sign-up cannot be limited to the owner, wenna's owner check is the only
control.**

## Rules for the agent

- **The user types every credential:** email, password, codes, captchas, MFA.
  Never read a code from the user's inbox.
- **No secrets in chat.** This setup needs none: the AuthKit domain and wenna's
  URL are public. Don't copy any API key or client secret a page shows.
- **Stop on drift.** A missing menu, a renamed toggle, a plan or card prompt:
  screenshot, say what differs, ask.
- **Change only what a step names.** No deletes, other environments or invites.
- **Read back each change** before moving on.

## Before starting

| Input | Check |
| --- | --- |
| wenna's public URL, stable: renaming the host breaks the resource | `curl https://<host>/api/health` answers |
| MCP endpoint, `https://<host>/api/mcp` | The URL AI apps will add |
| Where wenna's settings live | The user can open it |
| Owner's WorkOS user ID | Appears on the dashboard's Users page after the owner first signs in |

## Steps in WorkOS

1. **Account (user).** Open the WorkOS dashboard; the user signs up and
   verifies their email.
2. **Environment.** Pick the one wenna will use.
3. **AuthKit.** Done when: the environment has an AuthKit domain.
4. **Client registration.** Connect → Configuration: turn on **Client ID
   Metadata Document** (off by default) and **Dynamic Client Registration**.
   Done when: both read on after a reload.
5. **Resource indicator.** Add `https://<host>/api/mcp`; from its "…" menu make
   it the default. It must match wenna's URL exactly: no trailing slash. An API
   exists (`POST /user_management/authkit_oauth_resources`, `default: true`
   at creation only, no update), but needs an `sk_` key the user would have to
   hand over: prefer the dashboard.
6. **Copy the AuthKit domain.** Check:
   `https://<authkit domain>/.well-known/oauth-authorization-server` shows
   `"client_id_metadata_document_supported": true`.

## Configure wenna and verify

1. **Set** `WENNA_WORKOS_ISSUER=https://<authkit domain>`,
   `WENNA_WORKOS_RESOURCE=https://<host>/api/mcp` (exactly step 5's value) and
   `WENNA_WORKOS_OWNER_ID=<owner's user_… ID>` from the dashboard's Users page,
   after the owner has signed in once; restart. All three or none. Where
   `WENNA_PUBLIC_URL` is set (Tailscale Funnel), the resource is it +
   `/api/mcp`. Where settings live per path:
   [cloudflare.md § Before starting](cloudflare.md#before-starting).
2. **Discovery:** `https://<host>/.well-known/oauth-protected-resource` lists the
   AuthKit domain, and its `resource` matches step 5.
3. **Challenge:** `/api/mcp` with no token → 401 with `WWW-Authenticate` naming
   that metadata.
4. **Bearer still works:** the user runs a request with `WENNA_TOKEN`; it
   answers.
5. **An OAuth app:** add a custom connector with `https://<host>/api/mcp`,
   OAuth fields empty; the user signs in to AuthKit and approves.
6. **Prove it:** remember a test fact, find it from another app, forget it.
