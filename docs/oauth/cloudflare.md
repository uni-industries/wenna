# OAuth through Cloudflare Access: setup runbook

Owns: putting Cloudflare Access managed OAuth in front of one wenna instance,
by API where Cloudflare documents one, in the user's browser otherwise. The
alternative is [workos.md](workos.md); which one a hosting path uses:
[HOSTING.md § Choose a host](../../HOSTING.md#choose-a-host).

Reference: Cloudflare's
[managed OAuth docs](https://developers.cloudflare.com/cloudflare-one/access-controls/applications/http-apps/managed-oauth/).

## How it works

- With managed OAuth on, Access is the OAuth server for wenna's hostname:
  discovery, client registration, PKCE, sign-in, refresh.
- On each call Access swaps the app's token for a signed
  `Cf-Access-Jwt-Assertion` header. wenna verifies it against the team's keys and
  the app's AUD tag
  ([Cloudflare](https://developers.cloudflare.com/cloudflare-one/access-controls/applications/http-apps/authorization-cookie/validating-json/)).
- Access blocks any request without its own token, so bearer clients cannot use
  the Access hostname. They use a second, bearer-only hostname (step 6).
- Cloudflare terminates TLS and sees memory contents: already true with the
  Tunnel and Containers, new for Railway, Fly and Render.

## Rules for the agent

- **The user types every credential:** password, emailed codes, captchas, MFA.
  Hand the browser back and wait.
- **No secrets in chat.** The team domain and AUD tag are not secrets. API
  tokens, tunnel tokens and `WENNA_TOKEN` are: they live in files in `~/.wenna`.
- **Stop on drift.** A missing menu or field, a renamed setting, a plan or
  card prompt: say what differs, ask.
- **Change only what a step names.** Leave other DNS records, applications, WAF
  and bot settings alone.
- **Read back each change** before moving on.

## Before starting

Access protects any hostname Cloudflare serves. Where it sits per path:

| Path | Access hostname | Bearer hostname (step 6) | Domain needed |
| --- | --- | --- | --- |
| Containers | A custom domain on the Worker | The Worker's `workers.dev` address | Yes, for the Access hostname; see below |
| Tunnel, Mac or Docker | `<host>`, routed by the tunnel | A second tunnel hostname | Yes |
| Railway, Fly, Render | A custom domain on the platform, its Cloudflare DNS record proxied | The platform's own URL | Yes |

**Containers without a domain:** Access can sit on the `workers.dev`
hostname, but a Worker has only one, so nothing is left for header-only apps.
Use WorkOS there.

| Input | Mac | Docker Compose | Containers | Railway, Fly, Render |
| --- | --- | --- | --- | --- |
| Where settings live | `EnvironmentVariables` in `com.wenna.serve.plist` ([HOSTING.md](../../HOSTING.md#on-a-mac), "Change a setting") | `.env.wenna` beside `compose.yaml` | Worker secrets ([HOSTING.md](../../HOSTING.md#cloudflare-containers)) | The platform's variables |
| Restart | `launchctl bootout`, then `bootstrap` | The start command in HOSTING.md | `npx wrangler deploy` | Redeploy |
| Owner's email | The address that receives the sign-in code: ask the user | Same | Same | Same |

API calls below use a token the user created in the dashboard, read from
`~/.wenna/cloudflare-api-token`, with `Access: Apps and Policies Write` and
`Access: Organizations, Identity Providers, and Groups Write`
([HOSTING.md § Cloudflare Containers](../../HOSTING.md#cloudflare-containers),
which revokes it when these steps are done).

## Platform custom domains

Set these before step 3. The SSL/TLS encryption mode is the whole domain's
setting; tell the user before changing it.

### Railway

- Hobby plan: Free has no custom domains.
- `railway domain <host> --port 8080` prints a CNAME and a TXT record; both
  are required, or the domain answers 404.
- Encryption mode **Full**; Railway says Full (strict) does not work
  ([Railway](https://docs.railway.com/networking/domains/working-with-domains#cloudflare-configuration)). The
  Cloudflare→Railway hop is then encrypted but its certificate not checked.
- Only a first-level subdomain (`wenna.example.com`) can be proxied without
  Cloudflare's Advanced Certificate Manager.

### Render

1. `POST /services/{id}/custom-domains` with `{"name":"<host>"}`.
2. CNAME to `<service>.onrender.com`, **DNS only**, until Render shows the
   certificate issued; then proxied. During that window the domain bypasses
   Access (the token still guards it).
3. Remove any `AAAA` records; encryption mode **Full**.
4. Keep the `onrender.com` subdomain on: it is the bearer hostname.

### Fly.io

1. `fly certs add <host>`; `fly certs setup <host>` gives a `_fly-ownership`
   TXT record; add it and the A/AAAA or CNAME records, proxied.
2. Encryption mode **Full (strict)**; Flexible loops.
3. `fly certs check <host>`. If issuance stalls, Fly's fallback is a
   Cloudflare Origin Certificate (`fly certs import`), renewed by hand.

## Steps in Cloudflare

1. **Zero Trust (user).** If the account already has a team, reuse it: one
   per account, and its name need not mention wenna. Otherwise the user
   onboards in the dashboard: team name, the Free plan, a card (required, not
   charged). Done when: `<team>.cloudflareaccess.com` shows in Zero Trust
   settings.
2. **One-time PIN login method.** It must be added: Cloudflare's default is
   its own identity provider. API:
   `POST /accounts/{id}/access/identity_providers` with
   `type: "onetimepin"`. Done when: it is
   listed among the identity providers.
3. **Access application and policy.** A **self-hosted**, hostname-based app
   for wenna's hostname, path empty, never a Worker-wide one (it would cover
   the bearer hostname too), with one policy: **Allow**, include only the
   owner's email. API: `POST /accounts/{id}/access/apps`, `type:
   "self_hosted"`, `domain` the hostname, inline `policies`, and
   `oauth_configuration` (step 4); the response carries the `aud`. Later
   edits: GET, then PUT the full body. Done when: the app is listed with its
   hostname and policy.
4. **Managed OAuth** on (`oauth_configuration`; the app's Advanced settings
   in the dashboard):
   - **Allowed redirect URIs** (`dynamic_client_registration.allowed_uris`):
     `https://claude.ai/api/mcp/auth_callback`,
     `https://chatgpt.com/connector/oauth/*`,
     `https://chatgpt.com/connector_platform_oauth_redirect`.
     ChatGPT's come from [OpenAI](https://developers.openai.com/plugins/build/auth).
   - **Allow localhost** and **allow loopback clients:** on (the default), for
     apps on the user's computer.
   - **Access token lifetime:** default. **Grant session duration:** 2 weeks;
     the user signs in again at the end of it.
   - Done when: the settings read back.
5. **AUD tag:** from the create response, or the app's Advanced settings.
   Check: `https://<host>/.well-known/oauth-authorization-server` returns
   Cloudflare's metadata.
6. **Bearer hostname** (for apps that only send a header),
   pointing at the same wenna. Create **no** Access app for it.
   - Containers: the `workers.dev` address.
   - Mac (locally managed tunnel): e.g. `<name>-key.<domain>`, a second
     ingress rule in `~/.cloudflared/config.yml`, a copy of the first with the
     new hostname, above the 404 rule;
     `cloudflared tunnel route dns wenna <bearer host>`;
     `launchctl kickstart -k gui/$(id -u)/com.wenna.cloudflared`.
   - Docker Compose: a second public hostname on the tunnel.
   - Railway, Fly, Render: the platform's own URL.
   - On the user's domain: Browser Integrity Check off for it
     ([HOSTING § What every hosted path needs](../../HOSTING.md#what-every-hosted-path-needs)).
   - Done when: `https://<bearer host>/api/health` answers.

## Configure wenna and verify

1. **Set** `WENNA_ACCESS_TEAM_DOMAIN=<team>.cloudflareaccess.com`,
   `WENNA_ACCESS_AUD=<AUD tag>` and `WENNA_ACCESS_OWNER_EMAIL=<owner's email>`;
   restart. All three or none. Done when: the start log's `server listening`
   line shows `config.access.on=true` with that team domain and AUD.
2. **Bearer host, no token:** `/api/mcp` → 401 from wenna.
3. **Bearer host, with token:** answers (token from its file, never in chat).
4. **Access host, no token:** `/api/mcp` → 401 from Cloudflare with
   `WWW-Authenticate`.
5. **An OAuth app:** add a custom connector with `https://<host>/api/mcp`,
   OAuth fields empty; the user enters the emailed code and approves.
6. **Prove it:** remember a test fact, find it from another app, forget it.
