# Self-hosting wenna

Run your own `wenna serve` at an HTTPS address, so AI apps that connect by URL
reach your memory from anywhere. It is still one instance for one person: whoever
holds the token is you. Nobody else hosts it for you.

**For personal, low-risk use only.** On the internet your memory is guarded
by one token and nothing else; wenna lacks per-app tokens, rate limits,
encryption at rest and an access log. Read [SECURITY.md](SECURITY.md) first.

## Choose a host

Prices checked 2026-10-28 at the linked page; they change, so re-check before
the user commits.

| Path | Cost | When idle | OAuth-only apps | The user does |
|---|---|---|---|---|
| **Local only**: `wenna stdio` ([§ Apps on this computer](#apps-on-this-computer)) | Free | — | — | Nothing |
| **[Cloudflare Containers](#cloudflare-containers)**, recommended hosted path | Workers Paid $5/mo minimum; R2 free to 10 GB ([Workers](https://developers.cloudflare.com/workers/platform/pricing/), [Containers](https://developers.cloudflare.com/containers/platform/pricing/), [R2](https://developers.cloudflare.com/r2/pricing/)) | Sleeps; restores from R2 at the next request | Access on a domain; else WorkOS | 4, 5 with a domain |
| **[Railway](#railway)** | Hobby $5/mo, includes $5 usage ([pricing](https://railway.com/pricing)) | Never sleeps (Serverless left off) | WorkOS, or Access on a domain | 2, 3 with a domain |
| **[Render](#render)** | $7/mo instance + $0.25/GB/mo disk ([pricing](https://render.com/pricing)) | Never sleeps | WorkOS, or Access on a domain | 4, 5 with a domain |
| **[Fly.io](#flyio)** | $2.19/mo per 256 MB Machine running all month, in its cheapest regions; $0.15/GB/mo volume ([pricing](https://docs.fly.io/about/pricing)) | Machine stops; starts on the next request | WorkOS, or Access on a domain | 2, 3 with a domain |
| **[Your Mac + Tailscale Funnel + WorkOS](#tailscale-funnel-free)** | Free ([Tailscale](https://tailscale.com/pricing), [WorkOS](https://workos.com/pricing)) | The Mac must stay on | WorkOS | 3, plus WorkOS's |
| **[Your machine + Cloudflare Tunnel](#cloudflare-tunnel)** | A domain's yearly fee; Cloudflare free | The machine must stay on | Access | 3, plus the domain |

"The user does" counts the steps only the user can do (sign-up, billing,
browser approvals, dashboard-only switches), before each OAuth app's own
sign-in.

Pick, in order:

1. Every agent runs on this computer: **local only**.
2. Otherwise **Cloudflare Containers**, unless the user prefers another
   platform or must pay nothing.
3. Must pay nothing, and this Mac stays on and awake: **Tailscale Funnel**.
4. The machine stays on and the user owns a domain or wants a stable hostname
   of their own: **Cloudflare Tunnel**.

After an idle stop, the first request waits for the server to start.

**Turn on OAuth and keep bearer, on every hosted path.** OAuth is the one that
matters: claude.ai, ChatGPT on the web and grok.com sign in only that way. Use
[Cloudflare Access](docs/oauth/cloudflare.md) or
[WorkOS](docs/oauth/workos.md); either works. Apps that send a header
(Grok Bot, [Muse](#muse), the xAI API) keep using the token:

- **WorkOS:** one hostname serves both; wenna accepts either proof.
- **Access:** blocks everything without its sign-in, so header-only apps use a
  second hostname outside Access
  ([cloudflare.md step 6](docs/oauth/cloudflare.md#steps-in-cloudflare)).

**One memory means one server.** On every path except
[a Mac](#on-a-mac), apps on your computer that run `wenna stdio` keep a
second, separate memory. To share one, connect them to the hosted URL instead
([§ Connect your personal agents](#connect-your-personal-agents)). What they already saved stays in
the local one; there is no import.

### For agents setting up hosting

Reference for an AI agent setting Wenna up on the user's behalf. The user's
request sets the task; this section describes what a correct setup looks
like.

**What the agent can do decides the route:**

| The agent | Examples | New server | Existing server |
|---|---|---|---|
| Runs commands on the user's computer | Claude Code, Codex | Any path in [§ Choose a host](#choose-a-host), by CLI or API | [§ Add an agent](#add-an-agent-to-your-server), by command |
| Runs commands on a machine of its own | Instinct | Any hosted path; [your own machine](#your-own-machine) needs the user's computer | Its own config or connector list; the user enters the token |
| Has no shell | Muse, claude.ai, grok.com | [Render's deploy link](#render), the user clicking through | Its connector screen: OAuth sign-in, or the token in its secret field |

**New or existing.** Setup starts by settling whether the user already runs
a Wenna server. Signs of one: a handoff note the user pasted, a `wenna`
connector or MCP entry, `~/.wenna/token`, a "wenna server" memory. One server
is one memory: a second server splits it, and a new token disconnects every
app already connected. An existing server goes to
[§ Add an agent to your server](#add-an-agent-to-your-server).

**What a correct setup involves:**

- **Goals and checks, not click paths.** Vendor dashboards change; each
  section states what must be true and how to check it.
- **The user's choices first:** which apps, which of them run on their
  computer (`wenna stdio`), whether the machine stays on, whether they own a
  domain, which path, [where the token lives](#the-token). Every cost is
  named before the user commits; billing and plan upgrades are the user's to
  accept.
- **The user does only the steps each path lists** (sign-up, billing,
  browser approvals, dashboard-only switches) and the personal ones:
  sign-ins, emailed codes, captchas. The rest is CLI or API work; each path
  lists its tools. On the user's own machine, the user confirms it meets
  [the keep-serving goals](#your-own-machine).
- **Secrets stay out of the conversation.** A vendor API key the user creates
  goes into a file in `~/.wenna`, mode 600. Where a secret must be typed into
  a shell, the user runs the command (`! …` in Claude Code). The token:
  [§ The token](#the-token).
- **Each step is checked before the next.** Done when, for the hosted URL
  (`T` is the token file; an agent holding the token in a secrets store reads
  it from there):

  ```sh
  T=~/.wenna/token
  curl -s -o /dev/null -w '%{http_code}\n' https://<host>/api/health              # 200
  curl -s -o /dev/null -w '%{http_code}\n' -X POST https://<host>/api/mcp         # 401
  curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $(cat $T)" \
    https://<host>/api/export                                                     # 200
  ```

  Then end to end: each connected app saves a test memory; another app
  recalls it, or the hosted export holds it
  (`curl -sf -H "Authorization: Bearer $(cat $T)" https://<host>/api/export | gunzip -c | grep -c '<test fact>'`);
  the app forgets it. Local `wenna export` reads this computer's database,
  which is the hosted one only on [a Mac](#on-a-mac).
- **Last, the handoff:** [§ Hand off to the next agent](#hand-off-to-the-next-agent).

### The token

One random value; whoever holds it is the user. Where it lives is the
user's choice:

| Option | Where it lives | Fits |
|---|---|---|
| **[A. Password manager](#a-password-manager)**, recommended | The user's password manager; a file copy where a shell agent needs one | Every kind of agent |
| [B. OAuth first](#b-oauth-first) | As in A or C, used only by apps that cannot sign in with OAuth | Most apps sign in with OAuth |
| [C. One file](#c-one-file) | `~/.wenna/token` on the machine that set the server up | Every app runs on that machine, or an agent there hands it out |

The token never goes into a chat message, a URL or command output. An
existing token is in use: replacing it is rotation
([§ Keep your data](#keep-your-data)).

#### A. Password manager

1. **The user creates it:** a new entry named "Wenna", with a generated
   password of at least 48 characters, letters and digits only (symbols can
   break app configs).
2. **Into the server:** the user pastes it into the platform's secret
   setting (Render's `WENNA_TOKEN` prompt, a dashboard secret), or a shell
   agent feeds it from a file copy.
3. **A file copy**, for an agent with a shell: the user copies the entry, then
   on a Mac runs
   `! (umask 077 && mkdir -p ~/.wenna && pbpaste | tr -d '\n' > ~/.wenna/token && pbcopy < /dev/null)`.
   Elsewhere, the user saves it with an editor, mode 600.
   [§ From the file](#from-the-file) feeds it on.
4. **Into each app:** the user pastes it into the app's secret field (an API
   key field, a secure form, a secrets store), never into a message.

#### B. OAuth first

Apps that sign in with OAuth (claude.ai, ChatGPT on the web, grok.com) use
the OAuth URL; the user signs in with the provider, and the app never sees
the token. It needs a provider: [§ Choose a host](#choose-a-host). The token
stays for the server and for header-only apps (Muse, Instinct, Grok Bot, the
xAI API), kept as in A or C. With no header-only apps, nothing past the
server ever uses it.

#### C. One file

The token lives only in a file on the machine that set the server up:

```sh
mkdir -p -m 700 ~/.wenna
[ -e ~/.wenna/token ] || (umask 077 && openssl rand -hex 32 > ~/.wenna/token)
```

An app on another computer gets a copy of the file, carried by the user,
never through a chat. An app without a shell gets it from an agent on that
machine: [§ Give it to the user](#give-it-to-the-user).

#### From the file

| Into | From the file |
|---|---|
| A command's input | `< ~/.wenna/token`, or `{ printf 'WENNA_TOKEN='; cat ~/.wenna/token; } \|` for `NAME=value` input |
| A JSON body or file | `jq -n --rawfile t ~/.wenna/token '{… ($t\|rtrimstr("\n")) …}'` |
| A command's argument | `"$(cat ~/.wenna/token)"`, only where nothing else works (it shows in the process list) |
| A web form or an app's chat | The user pastes it: from the password manager (A), or from the clipboard ([§ Give it to the user](#give-it-to-the-user)) |

#### Give it to the user

For an app that takes the token in its own screen, from an agent on a machine
holding the file. The user never looks for it, and the agent never sees it.

1. **It is this host's:**

   ```sh
   { printf 'Authorization: Bearer '; cat ~/.wenna/token; } |
     curl -s -o /dev/null -w '%{http_code}\n' -H @- https://<host>/api/export   # 200
   ```

   No file, or 401: this machine does not hold this host's token. It is in
   the user's password manager (A) or on the machine that set the host up
   (C). A new one is never made here: that is rotation.
2. **Onto the clipboard**, without the file's trailing newline. On a Mac:
   `tr -d '\n' < ~/.wenna/token | pbcopy`. Elsewhere the user opens the file
   and copies it.
3. **The user pastes it** where the app asks (its field name), and confirms.
4. **The clipboard is cleared:** `pbcopy < /dev/null`.

### What every hosted path needs

| Setting | Value |
|---|---|
| `WENNA_TOKEN` | From the token file. Never commit it. |
| `WENNA_BEHIND_TLS_PROXY` | `true`: you assert that HTTPS is terminated in front of wenna. |
| Storage | A persistent disk at `/data` (Cloudflare: an R2 replica). Without it wenna refuses to start rather than keep your memory on a disk a restart erases. |
| Instances | Exactly one. The database is one SQLite file. |
| Stop grace | Longer than wenna's shutdown timeout; each path sets it. |
| Browser Integrity Check | Off for `<host>` paths under `/api/`, where `<host>` is on a domain whose DNS is on Cloudflare. Without it some apps cannot connect (403, error 1010). A Configuration Rule on the zone: match `http.host eq "<host>" and starts_with(http.request.uri.path, "/api/")`, setting Browser Integrity Check off. API: GET, then PUT `/zones/{zone_id}/rulesets/phases/http_config_settings/entrypoint` with existing rules kept, adding `action: "set_config"`, `action_parameters: {"bic": false}`. It weakens a security setting: the user approves deploying it. Check: the app lists wenna's tools. |

The image refuses to start until `WENNA_BEHIND_TLS_PROXY=true`. Set it only
where requests from the internet reach wenna through HTTPS. Never set it with
wenna's port published directly to a network (`docker run -p 8080:8080`): wenna
itself speaks plain HTTP. Publishing to the machine only
(`-p 127.0.0.1:8080:8080`) is safe.

Two things the proxy does not cover:

- **Use the `https://` URL in every app.** A platform's redirect from `http://`
  answers only after the app has sent the request, token included, in the
  clear.
- **The platform's private network reaches wenna directly**, without HTTPS.
  Other services in the same project can connect; the token still guards
  every request.

### Disk

The trigram index is on by default (`WENNA_TRIGRAM`): recall matches text
inside a word ("foods" finds "atlasfoods.com"). It costs disk: the
database grows to several times the size of your memories' text. Small for a person's notes,
but **watch the volume's usage once your memory gets large**, and grow the
volume before it fills. To trade that match for space, set
`WENNA_TRIGRAM=false`; the index already on disk stays until you export and
start a fresh database.

## Cloudflare Containers

Everything in one Cloudflare account: the [cloudflare/](cloudflare/) Worker
runs wenna in a container under [Litestream](https://litestream.io). The
container's disk is erased whenever it stops, so at start it restores the
database from an R2 bucket the user owns, then streams every change back.

Needs: a clone of this repo; Node with npm; Docker running (`docker info`);
`jq`; the token file.

The user does:

1. Sign up for Cloudflare.
2. Billing, in one dashboard session: Workers Paid, R2 activated ("purchase
   R2"), a card. With Access, also Zero Trust onboarding: team name, the Free
   plan, a card (required, not charged).
3. Click Allow on `npx wrangler login`.
4. Create a bootstrap API token in the dashboard (no other way exists) and
   save it to `~/.wenna/cloudflare-api-token`: `Account API Tokens Write`; with
   Access also `Access: Apps and Policies Write` and `Access: Organizations,
   Identity Providers, and Groups Write`.
5. Custom domain only: own the domain, its DNS on Cloudflare.

Steps, from `cloudflare/` (`npm ci` first):

1. **Bucket:** `npx wrangler r2 bucket create wenna`.
2. **R2 key**, scoped to that bucket only: with the bootstrap token,
   `POST /accounts/{id}/tokens` with policy `Workers R2 Storage Bucket Item
   Write` on that bucket
   ([Cloudflare](https://developers.cloudflare.com/r2/api/tokens/)). The
   Access Key ID is the token's `id`; the Secret Access Key is the SHA-256 hex
   of its `value`. Write both straight into the secrets file below, never to
   output.
3. **First deploy, with every secret in one file.** A JSON file `$f` in
   `~/.wenna` (mode 600) with `WENNA_TOKEN` (from the token file), `R2_ACCOUNT_ID`,
   `R2_BUCKET`, `R2_ACCESS_KEY_ID`, `R2_SECRET_ACCESS_KEY`, plus any optional
   settings below. Deploy, and delete the file whether or not the deploy
   succeeded, keeping its exit status:
   `(trap 'rm -f "$f"' EXIT; npx wrangler deploy --secrets-file "$f")`.
   Check the file is gone. JSON, not `.env`: wrangler expands `$` in a dotenv
   file and lets the shell environment override it. The first build takes a
   while on an Apple Silicon Mac: it builds for amd64.
4. **Host:** the `*.workers.dev` address `wrangler deploy` prints, or a custom
   domain on the Worker (`routes` with `custom_domain: true`, or the API).
   Always Use HTTPS is not needed: the Worker refuses `http://` with 403.
5. **Check** as in [§ For agents](#for-agents-setting-up-hosting). If
   `/api/mcp` with the token answers 403 `invalid Host header`, set
   `WENNA_PUBLIC_URL` to that host's `https://` origin and redeploy; one
   admitted host then cannot cover both an Access custom domain and
   `workers.dev`.
6. **Revoke the bootstrap token** once the R2 key exists and, with Access,
   [cloudflare.md](docs/oauth/cloudflare.md)'s Cloudflare steps are done: it
   can mint any account token. Revoke it (by API with itself, or the user in
   the dashboard); check it no longer verifies
   (`GET /accounts/{id}/tokens/verify`); then delete
   `~/.wenna/cloudflare-api-token` and check it is gone.

**Change a setting later** with `npx wrangler secret put <NAME>` (value on
stdin), then `npx wrangler deploy`. Use secrets, not dashboard variables:
`wrangler deploy` erases variables and keeps secrets. Secrets are additive: nothing removes one but
`npx wrangler secret delete <NAME>`. To turn an OAuth group off, delete each
of its variables that way, then redeploy.

Optional settings, all secrets:

| Setting | Value |
|---|---|
| `WENNA_ACCESS_TEAM_DOMAIN`, `WENNA_ACCESS_AUD`, `WENNA_ACCESS_OWNER_EMAIL` | Cloudflare Access ([cloudflare.md](docs/oauth/cloudflare.md)). All three or none. |
| `WENNA_WORKOS_ISSUER`, `WENNA_WORKOS_RESOURCE`, `WENNA_WORKOS_OWNER_ID` | WorkOS ([workos.md](docs/oauth/workos.md)). All three or none. |
| `WENNA_PUBLIC_URL` | The https origin apps use. With WorkOS, `WENNA_WORKOS_RESOURCE` is it + `/api/mcp`. |
| `WENNA_SLEEP_AFTER` | Idle time before sleeping: a whole number then `s`, `m` or `h` (`30m`, `2h`). Default `10m` (the library's). Longer: fewer slow first requests, a bigger bill. |

Failures: a missing required secret or a malformed `WENNA_SLEEP_AFTER` →
the Worker answers 503 and starts nothing. A partial OAuth group or a wrong
R2 key → wenna refuses to start (`npx wrangler tail` shows why).

**OAuth and bearer, both.** Turn OAuth on (the apps that matter most sign in
that way) without losing the bearer token for header-only apps:

- **With a custom domain:** Access ([cloudflare.md](docs/oauth/cloudflare.md))
  on the domain, as a hostname-based Access app, never the Worker-wide one
  (that covers every hostname). The `workers.dev` address stays the bearer
  hostname.
- **Without one:** WorkOS ([workos.md](docs/oauth/workos.md)) on the
  `workers.dev` address. wenna accepts the bearer token and WorkOS sign-in on
  the same hostname, so one address serves both. Access on `workers.dev` would
  block bearer apps.

What to know:

- **Size:** the default instance type, `lite`, has 256 MiB of memory and a
  2 GB disk (checked 2026-10-28,
  [Cloudflare](https://developers.cloudflare.com/containers/platform/limits/));
  the restored database must fit.
- **Not always running.** Cloudflare restarts hosts on its own schedule;
  each start restores from R2 first, so the first request after one is slow.
- **The last second can be lost.** Changes reach R2 about once a second, and
  once more when the container stops. A crash loses changes not yet sent; so
  does a stop within about a second of the container starting.
- **One deploy per bucket.** Two instances writing one replica corrupt it.
  The Worker allows one container; never point a second deploy at the same
  bucket.
- **The bucket holds your whole memory, unencrypted**, readable by anyone with
  the R2 key or access to the Cloudflare account.
- **Cost beyond $5:** compute is billed only while running, inside the
  included amounts at `lite` with the default sleep.

## Railway

Needs: the Railway CLI (`railway --version`); a clone, run from its root; the
token file. No GitHub link: `railway up` uploads the local directory.

The user does: sign in (`railway login`, which is also sign-up); Hobby and a
card; DNS records for a custom domain.

```sh
railway init --name wenna --json
railway add --service wenna
railway variable set WENNA_TOKEN --stdin --skip-deploys < ~/.wenna/token
railway variable set WENNA_BEHIND_TLS_PROXY=true PORT=8080 \
  RAILWAY_DEPLOYMENT_DRAINING_SECONDS=15 --skip-deploys
railway volume add --mount-path /data
railway up --ci
railway domain --json            # https://<name>.up.railway.app
```

- **Plan:** Hobby.
- **Grace:** `RAILWAY_DEPLOYMENT_DRAINING_SECONDS=15`. Railway's default is 0,
  an immediate kill.
- **One instance:** a service with a volume cannot have replicas.
- **Sleep: leave Serverless off** (the default). Railway warns that the first
  request to a slept service may return a 502, which an app may report as a
  failed connection.
- **Hostname:** renaming the `up.railway.app` domain breaks WorkOS and every
  app.
- **Redeploys** with a volume have a short outage. Volume backups are a
  dashboard switch; export stays the backup that leaves Railway.
- **Access on a custom domain:** [cloudflare.md](docs/oauth/cloudflare.md#railway).

## Render

Needs: `curl` and `jq`; the token file.

The user does: sign up; a card ($1 check, refunded); create an API key and
save it to `~/.wenna/render-key`; connect GitHub; DNS records for a custom domain.

One API call creates the service, its disk at `/data` and both settings
([API](https://api-docs.render.com/reference/create-service)):

```sh
K=~/.wenna/render-key; API=https://api.render.com/v1
H=(-H "Authorization: Bearer $(cat $K)" -H 'Content-Type: application/json')
OWNER=$(curl -sf "${H[@]}" $API/owners | jq -r '.[0].owner.id')
jq -n --rawfile t ~/.wenna/token --arg o "$OWNER" '{
  type:"web_service", name:"wenna", ownerId:$o,
  repo:"https://github.com/uni-industries/wenna", branch:"main",
  envVars:[{key:"WENNA_TOKEN",value:($t|rtrimstr("\n"))},
           {key:"WENNA_BEHIND_TLS_PROXY",value:"true"}],
  serviceDetails:{runtime:"docker", plan:"0.5c-512mb", numInstances:1,
    healthCheckPath:"/api/health",
    disk:{name:"wenna-data", mountPath:"/data", sizeGB:1},
    envSpecificDetails:{dockerfilePath:"./Dockerfile"}}}' \
| curl -sf "${H[@]}" -X POST $API/services -d @- | jq '{id:.service.id, url:.service.serviceDetails.url}'
```

Done when the response names the service; if `url` is empty, its
`onrender.com` URL is in `GET /services/{id}`.

Without a shell, or as an alternative: the Blueprint from
[render.yaml](render.yaml), opened in the user's browser at
`https://render.com/deploy?repo=https://github.com/uni-industries/wenna`.
It needs the Git connection, a paid instance type, and prompts once for
`WENNA_TOKEN`, which the user pastes from the password manager
([A](#a-password-manager)). Every push to the linked branch then redeploys.

- **Plan:** any paid instance; Free has no disk and sleeps. Paid does not
  sleep.
- **Grace:** fixed at 30 s for a service with a disk; nothing to set.
- **Deploys** stop the old instance before starting the new: a few seconds
  down.
- **Rotate:** `PUT /services/{id}/env-vars/WENNA_TOKEN` with the new value, then
  `POST /services/{id}/deploys`.
- **Backups:** export regularly.
- **Access on a custom domain:** [cloudflare.md](docs/oauth/cloudflare.md#render).

## Fly.io

Needs: `flyctl` (`fly version`); a clone, run from its root; the token file.
[fly.toml](fly.toml) sets the disk, the size and idle stop.

The user does: sign up (`fly auth signup`) or sign in (`fly auth login`), in a
browser; add a card. Without one the trial stops each Machine after
5 minutes.

```sh
fly orgs list                                         # the org slug
fly launch --copy-config --no-deploy --no-github-workflow \
  --name <app> --region <region> --org <org> --yes
fly volumes create wenna_data --size 1 --region <region> --yes
{ printf 'WENNA_TOKEN='; cat ~/.wenna/token; } | fly secrets import --stage
fly deploy --ha=false
fly status                                            # exactly one Machine
```

Host: `https://<app>.fly.dev`, fixed for the app's life. `fly launch` may
rewrite fly.toml in the clone (name, region).

- **One volume, one Machine.** Never `fly scale count` or `fly machine clone`:
  each makes a new, empty volume, a second memory.
- **One host.** The volume lives on one server's drive: a host failure is an
  outage; a drive failure loses writes since the last daily snapshot. Export
  regularly.
- **Idle:** the Machine stops when unused and starts on the next request;
  while stopped, only the volume and root filesystem are billed.
- **Memory:** fly.toml pins 256 MB. Raise it if Fly
  reports out-of-memory restarts.
- **Access on a custom domain:** [cloudflare.md](docs/oauth/cloudflare.md#flyio).

## Your own machine

No platform: the memory stays on your hardware.

**The machine must keep serving.** The user sets it so that it:

- never sleeps while on power (the display may);
- starts again by itself after a power cut;
- is running the services after a restart. On a Mac they are launch agents,
  which run only while the user is logged in: automatic login does it, but
  FileVault turns automatic login off, so then someone must log in after
  every restart. With Docker, the engine must start at boot; Docker Desktop
  starts only at login.

### On a Mac

Needs: wenna installed ([README](README.md#install)), at
`~/.local/bin/wenna`.

Run `wenna serve` natively, so URL apps and stdio apps share one database
(`~/.wenna/wenna.db`). Do not use the Docker setup on a Mac: its database lives
in a Docker volume, a second, separate memory. Then expose it with
[Tailscale Funnel](#tailscale-funnel-free) or
[Cloudflare Tunnel](#cloudflare-tunnel).

1. **Token**: [§ The token](#the-token). It also creates `~/.wenna`, where the
   logs below go.

2. **`wenna serve` as a login service** (loopback; no proxy setting needed):

   ```sh
   cat > ~/Library/LaunchAgents/com.wenna.serve.plist <<EOF
   <?xml version="1.0" encoding="UTF-8"?>
   <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
   <plist version="1.0"><dict>
     <key>Label</key><string>com.wenna.serve</string>
     <key>ProgramArguments</key><array>
       <string>/bin/sh</string><string>-c</string>
       <string>WENNA_TOKEN="\$(cat "\$HOME/.wenna/token")" exec "\$HOME/.local/bin/wenna" serve</string>
     </array>
     <key>RunAtLoad</key><true/>
     <key>KeepAlive</key><true/>
     <key>StandardErrorPath</key><string>$HOME/.wenna/serve.log</string>
   </dict></plist>
   EOF
   launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.wenna.serve.plist
   curl -s http://127.0.0.1:8080/api/health   # {"status":"ok"}
   ```

   The token is read from the file at start, so it is never written into the
   plist.

**Upgrade:** rerun the install script, or build from source to a temporary
path and move it over: `make build OUT="$HOME/.local/bin/wenna.new" && mv
~/.local/bin/wenna.new ~/.local/bin/wenna`. Never build straight onto
`~/.local/bin/wenna`: apps run `wenna stdio` from it, and overwriting it in place
can kill them. Then `launchctl kickstart -k gui/$(id -u)/com.wenna.serve`.

**Change a setting** (e.g. OAuth): add it to the plist's
`EnvironmentVariables` dict, then `launchctl bootout gui/$(id -u)/com.wenna.serve`
and bootstrap it as in step 2; `kickstart -k` does not reread the plist.

```sh
P=~/Library/LaunchAgents/com.wenna.serve.plist
/usr/libexec/PlistBuddy -c 'Add :EnvironmentVariables dict' "$P"   # once
/usr/libexec/PlistBuddy -c 'Add :EnvironmentVariables:<NAME> string <value>' "$P"
```

**Rotate the token:** overwrite `~/.wenna/token` (`(umask 077 && openssl rand
-hex 32 > ~/.wenna/token)`), then
`launchctl kickstart -k gui/$(id -u)/com.wenna.serve`, then update every app.

### Tailscale Funnel (free)

Funnel gives the Mac a public HTTPS address, `https://<machine>.<tailnet>.ts.net`,
with no domain. WorkOS adds OAuth; header-only apps use the same address with
the token.

Needs: [wenna serve on the Mac](#on-a-mac); Homebrew.

The user does: sign in to Tailscale at the URL `tailscale up` prints (this
creates the free tailnet); approve Funnel at the URL `tailscale funnel`
prints; turn off key expiry for the machine in the Tailscale admin console
(otherwise the key expires and Funnel stops); WorkOS's own steps
([workos.md](docs/oauth/workos.md)).

```sh
brew install --formula tailscale        # the open-source daemon
sudo brew services start tailscale
sudo tailscale up
sudo tailscale set --hostname=wenna      # pins the name in the URL
sudo tailscale funnel --bg 8080         # persists across reboots
tailscale funnel status                 # shows https://wenna.<tailnet>.ts.net
```

Then set `WENNA_PUBLIC_URL=https://wenna.<tailnet>.ts.net` in the plist
([Change a setting](#on-a-mac)): Funnel forwards the public hostname, and
wenna refuses it on loopback without this. Then WorkOS, with
`WENNA_WORKOS_RESOURCE` = `WENNA_PUBLIC_URL` + `/api/mcp`. Public DNS for a
new name can lag; if it does not resolve, wait and retry. **Check** as in
[§ For agents](#for-agents-setting-up-hosting).

- **Which Tailscale:** the open-source daemon. If a Tailscale app is already
  installed, ask before adding the daemon.
- **The machine name is public** (certificate transparency logs). Renaming the
  machine or tailnet changes the URL, breaking WorkOS and every app.

### Cloudflare Tunnel

A stable hostname on a domain the user owns (a yearly fee) with its DNS on
Cloudflare. No open ports; Cloudflare serves HTTPS through an outbound
tunnel. OAuth: Cloudflare Access.

Needs: [wenna serve on the Mac](#on-a-mac); `cloudflared`
(`cloudflared --version`; e.g. `brew install cloudflared`).

The user does: `! cloudflared tunnel login` (a browser, to pick the domain);
the Always Use HTTPS switch; approving the
[Browser Integrity Check rule](#what-every-hosted-path-needs).

`<host>` is a subdomain of the user's domain (e.g. `wenna.example.com`).

1. **Tunnel.** First check `cloudflared tunnel list`: if a tunnel named `wenna`
   exists, ask the user before reusing it or pick another name.
   `tunnel create` prints the path of the tunnel's credentials file
   (`~/.cloudflared/<tunnel id>.json`); `credentials-file` names that file,
   never a glob: other tunnels' files can sit beside it.

   ```sh
   cloudflared tunnel login
   cloudflared tunnel create wenna
   cloudflared tunnel route dns wenna <host>
   cat > ~/.cloudflared/config.yml <<EOF
   tunnel: wenna
   credentials-file: $HOME/.cloudflared/<tunnel id>.json
   ingress:
     - hostname: <host>
       service: http://127.0.0.1:8080
       originRequest:
         httpHostHeader: localhost
     - service: http_status:404
   EOF
   cat > ~/Library/LaunchAgents/com.wenna.cloudflared.plist <<EOF
   <?xml version="1.0" encoding="UTF-8"?>
   <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
   <plist version="1.0"><dict>
     <key>Label</key><string>com.wenna.cloudflared</string>
     <key>ProgramArguments</key><array>
       <string>$(command -v cloudflared)</string><string>tunnel</string><string>run</string>
     </array>
     <key>RunAtLoad</key><true/>
     <key>KeepAlive</key><true/>
     <key>StandardErrorPath</key><string>$HOME/.wenna/cloudflared.log</string>
   </dict></plist>
   EOF
   launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.wenna.cloudflared.plist
   launchctl list | grep com.wenna   # both lines start with a PID, not "-"
   ```

   `httpHostHeader: localhost` is required: wenna refuses a loopback connection
   that names another host (a defence against DNS rebinding), so without it
   every MCP request gets 403. The same holds for any proxy on the machine
   that cannot rewrite the host; then set `WENNA_PUBLIC_URL` instead.

   Use the launch agent above, not `cloudflared service install`.

2. **Always Use HTTPS** on for the domain. Otherwise `http://<host>` reaches
   wenna too. The switch covers every hostname on the domain: tell the user.

3. **Check** as in [§ For agents](#for-agents-setting-up-hosting).

- To check `<host>` is free, read the domain's DNS records in the dashboard.
  Do not look the name up: `dig` answers for any name on a Cloudflare zone, and
  a resolver caches the miss for up to an hour, so `<host>` then fails to
  resolve on this machine after the tunnel creates it.
- **Remove:** `launchctl bootout` both agents (`gui/$(id -u)/com.wenna.serve`,
  `gui/$(id -u)/com.wenna.cloudflared`) and delete their plists,
  `cloudflared tunnel delete wenna`, and the DNS record.

### On another server, with Docker

For a machine that is not a Mac. Needs: Docker with Compose
(`docker compose version`); a clone, run from its root; a domain on
Cloudflare.

[compose.yaml](compose.yaml) runs wenna and `cloudflared` side by side, with
the database in a Docker volume.

1. **Token**, on the server: [§ The token](#the-token).
2. Create a remotely-managed tunnel and copy its token. The user saves it
   to `~/.wenna/tunnel-token`, mode 600, never through the chat: on a Mac,
   after copying it, `(umask 077 && pbpaste > ~/.wenna/tunnel-token)`;
   elsewhere in an editor.
3. Add a public hostname for the tunnel, service `http://wenna:8080`.
4. Turn on Always Use HTTPS, as above.
5. Start it, and again with the same command after any change. Other settings
   (e.g. OAuth) go in `.env.wenna` beside compose.yaml, one `NAME=value` per
   line.

   ```sh
   WENNA_TOKEN="$(cat ~/.wenna/token)" TUNNEL_TOKEN="$(cat ~/.wenna/tunnel-token)" \
     docker compose up -d
   ```

   To skip the build, set `image: ghcr.io/uni-industries/wenna:latest` in
   place of `build: .` in compose.yaml.
6. **Check** as in [§ For agents](#for-agents-setting-up-hosting).

## Not supported

A platform that neither keeps files across restarts nor runs a container
image cannot run wenna: the memory is a file. Serverless function hosts are
this kind. Cloudflare Containers works only through
[its own setup](#cloudflare-containers).

## Add an agent to your server

Connecting one more app to a server the user already runs. Nothing here
creates a server or a token.

1. **The server's facts:** from the user's handoff note; else a "wenna
   server" memory, recalled by an app already connected; else the URL from
   the user.
2. **It is up:** `GET https://<host>/api/health` is 200, and `POST
   https://<host>/api/mcp` without a token is 401. On an Access hostname
   both are 401: Access answers first.
3. **The address.** An app that signs in with OAuth (claude.ai, ChatGPT on
   the web, grok.com) uses the OAuth URL, and the user signs in. Any other
   app uses the bearer URL (the second hostname when Access is on) and the
   token.
4. **Connect**, by what the agent can do:
   - **Commands on the user's computer** (Claude Code, Codex):
     [§ Connect your personal agents](#connect-your-personal-agents), the
     token from `~/.wenna/token`. No file there: a copy from the password
     manager ([A](#a-password-manager), step 3), or from the machine that
     holds it ([C](#c-one-file)). Apps on this computer use the hosted URL,
     never `wenna stdio`.
   - **Commands on a machine of its own** (Instinct): the server goes into
     its own config or connector list; the user enters the token into its
     secrets store. [§ Instinct](#instinct).
   - **No shell** (Muse, claude.ai, grok.com, Grok Bot): the app's connector
     screen; the user completes the OAuth sign-in, or pastes the token into
     its secret field.
     [§ Apps set up in their own chat](#apps-set-up-in-their-own-chat).
5. **Check:** the new app recalls "wenna server", then saves and forgets a
   test memory.
6. **Hand off** again, the new app added.

### Hand off to the next agent

At the end of every setup, and whenever the user asks how to add an app.

1. **The server, as a memory**, so any connected app can rebuild the note.
   Recall "wenna server"; update that memory if one exists, else remember
   one: `User's Wenna server: MCP URL https://<host>/api/mcp; OAuth via
   <Cloudflare Access | WorkOS | none>; bearer-only apps use
   https://<bearer host>/api/mcp; token kept <in the user's password manager
   | in ~/.wenna/token on <machine>>.` Keywords: `wenna`, `server`, `setup`.
   Never the token. An agent with wenna's tools uses them; a shell agent
   without them calls them over HTTP:

   ```sh
   wenna_call() { # tool name, JSON arguments
     { printf 'Authorization: Bearer '; cat ~/.wenna/token; } |
       curl -sf -H @- -H 'Content-Type: application/json' \
         -H 'Accept: application/json, text/event-stream' \
         --data "$(jq -nc --arg n "$1" --argjson a "$2" \
           '{jsonrpc:"2.0",id:1,method:"tools/call",params:{name:$n,arguments:$a}}')" \
         https://<bearer host>/api/mcp
   }
   wenna_call recall '{"query":"wenna server"}'
   ```

2. **The note**, filled in, for the user to paste into the next app. It
   carries everything the app needs, so it fetches nothing:

   ```text
   Connect yourself to my Wenna server. It already exists: don't set up a
   new server or make a new token. Wenna is the memory my AI apps share.
   MCP server (Streamable HTTP): https://<host>/api/mcp
   Sign-in: <Cloudflare Access | WorkOS> OAuth. Without OAuth, use
   https://<bearer host>/api/mcp with the header "Authorization: Bearer <token>".
   The token is <in my password manager, entry "Wenna" | in ~/.wenna/token on <machine>>.
   I'll put it into your secret or API key field myself; never ask for it in chat.
   Tools: remember, recall, update, forget, list. The server's initialize
   response carries its usage rules; follow them.
   Check: recall "wenna server", then save a test memory and forget it.
   Already connected: <apps>
   Setup reference, if needed: https://trywenna.com/llms.md
   ```

## Connect your personal agents

Your endpoint is `https://<your host>/api/mcp` with the header
`Authorization: Bearer <token>`, the token from [its file](#the-token).

```sh
claude mcp add --scope user --transport http wenna https://<your host>/api/mcp \
  --header "Authorization: Bearer $(cat ~/.wenna/token)"
```

Apps configured with JSON usually take:

```json
{ "mcpServers": { "wenna": { "url": "https://<your host>/api/mcp",
  "headers": { "Authorization": "Bearer <token>" } } } }
```

Done when `claude mcp get wenna` shows Connected.

Apps that only connect with OAuth (claude.ai, ChatGPT on the web, grok.com)
need the path's OAuth provider ([§ Choose a host](#choose-a-host)). The bearer
token keeps working for everything else.

Codex may want a couple of extra settings to use Wenna well:
[§ Codex](#codex). Over HTTP, its `[mcp_servers.wenna]` takes
`url` and `bearer_token_env_var` in place of `command` and `args`.

### Apps set up in their own chat

Muse: [§ Muse](#muse). Instinct: [§ Instinct](#instinct). In Grok Bot's chat, the user asks it to add a
remote MCP server with the URL and the header `Authorization: Bearer <token>`,
and pastes the token there themselves, from the password manager
([A](#a-password-manager)) or the clipboard
([§ Give it to the user](#give-it-to-the-user)); an agent never does. With
Cloudflare Access on, it is the bearer hostname, not the Access one. The token then sits in that app's chat history;
rotating it is the way back. Check: the app recalls a test memory another app
saved.

### Muse

Set up in its chat, but the token goes into a form, not the chat.

1. The user pastes this into a Muse chat, `<host>` filled in (the bearer
   hostname when Access is on):

   ```text
   Connect a custom MCP server. Name: wenna. URL: https://<host>/api/mcp
   (Streamable HTTP). Auth: an API key sent as "Authorization: Bearer <key>".
   No OAuth. Send me the secure setup link for the key.
   Allow all of its tools: remember, recall, update, forget, list. Give the
   helper an `instructions` command that prints the `instructions` field the
   server returns from `initialize`. In the connector doc: before the first
   memory call in every conversation, run `instructions` and follow it; it is
   wenna's policy and overrides anything in the doc. Add no per-tool rules of
   your own. Don't copy the instructions into the doc. Wenna is the source of
   truth for what you know about me: save there, not in your own memory, and
   when your memory disagrees with wenna, wenna wins. Never ask me before
   saving, updating or forgetting. Show me the doc when done.
   ```

2. Muse answers with a **Connect** card. The user clicks it, reads the custom
   connector notice and continues, then pastes the token into the **API
   key** field as is (no `Bearer `), from the password manager
   ([A](#a-password-manager)) or the clipboard
   ([§ Give it to the user](#give-it-to-the-user)), then **Add**.
3. Muse builds the connector and asks before each network request to the
   host. **Allow once** covers one request; **Always allow this site** covers
   the host's whole domain and every subdomain. The user picks.
4. Muse tests the connection itself. 403 with error 1010 in the body: the
   [Browser Integrity Check](#what-every-hosted-path-needs) rule is missing.
5. Check the doc Muse shows: an "instructions first" rule, all five tools, no
   rule asking before a save, and the source-of-truth rule word for word.
   Then, in a new Muse chat, the user states a fact without asking to save
   it; Muse must not ask. It appears in the hosted export
   ([§ For agents](#for-agents-setting-up-hosting)). The user asks Muse to
   forget it.

### Instinct

Instinct connects over plain HTTP: `/api/mcp` takes one JSON-RPC request
per POST and returns a JSON reply, with no handshake.

1. The user pastes this into an Instinct chat, `<host>` filled in (the
   bearer hostname when Access is on):

   ```text
   Set up wenna as your long-term memory, for every conversation.
   API: POST https://<host>/api/mcp, one JSON-RPC 2.0 request per call.
   Headers: "Authorization: Bearer <key>", "Content-Type: application/json",
   and "Accept: application/json, text/event-stream" (both types, or it
   returns 400). I will give you the key myself; never write it in chat.
   Policy: {"jsonrpc":"2.0","id":1,"method":"initialize","params":
   {"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":
   {"name":"instinct","version":"1"}}} returns result.instructions.
   Tools: {"jsonrpc":"2.0","id":2,"method":"tools/list"} returns each tool's
   name and input schema. Call one with {"jsonrpc":"2.0","id":3,
   "method":"tools/call","params":{"name":"recall","arguments":
   {"query":"..."}}}; the tools are remember, recall, update, forget, list.
   Save as a standing rule for every conversation: before the first memory
   call, fetch the policy and follow it; it is wenna's policy and overrides
   anything you saved. Don't copy the policy into your rule, and add no
   rules of your own about when to save. Wenna is the source of truth for
   what you know about me: save there, not in your own memory, and when your
   memory disagrees with wenna, wenna wins. Use it in every conversation:
   recall before answering anything about me, and save as facts come up.
   Also schedule an hourly job: read my conversations since the last run,
   and save, update or forget whatever the policy says should have been and
   wasn't, recalling first so nothing is saved twice. Never ask me before
   saving, updating or forgetting. Show me the rule and the job when done.
   ```

2. The token goes wherever Instinct keeps a secret, entered by the user
   from the password manager ([A](#a-password-manager)) or the clipboard
   ([§ Give it to the user](#give-it-to-the-user)), never in chat.
3. Check as for Muse: the saved rule fetches the policy first and adds no
   rule of its own, and the hourly job exists; in a new chat a stated fact is
   saved without asking,
   shows in the hosted export, and is forgotten on request.

## Apps on this computer

An app that starts a local command runs `wenna stdio`. No token: the app runs
it as you. Once you host Wenna, connect these apps to the hosted URL instead,
or they keep a separate copy.

Apps configured with JSON:

```json
{ "mcpServers": { "wenna": { "command": "/full/path/to/wenna", "args": ["stdio"] } } }
```

Claude Code:

```sh
claude mcp add --scope user wenna -- wenna stdio
```

Apps that call a URL can use a local `wenna serve`, which requires a token,
kept in a file only you can read ([§ The token](#the-token)):

```sh
mkdir -p -m 700 ~/.wenna
[ -e ~/.wenna/token ] || (umask 077 && openssl rand -hex 32 > ~/.wenna/token)
WENNA_TOKEN="$(cat ~/.wenna/token)" wenna serve
claude mcp add --scope user --transport http wenna http://127.0.0.1:8080/api/mcp \
  --header "Authorization: Bearer $(cat ~/.wenna/token)"
```

By default `wenna serve` listens on this machine only and refuses any other address: it
speaks plain HTTP, so the token and your memories would cross the network
unencrypted. To reach it from another machine, put a TLS proxy or an SSH
tunnel in front of it, or self-host it on a platform that serves HTTPS:
[§ Choose a host](#choose-a-host). A proxy on the same machine must send
`Host: localhost`, or MCP requests are refused. A second `wenna serve` on the
same port refuses to start.

### Codex

Codex may not pass a server's instructions to its model, and may ask before
each write. If so, give it wenna's instructions as its own and approve the
tools, in `~/.codex/config.toml`:

```toml
# The `Instructions` text in backend/internal/mcpserver/mcpserver.go. If you
# already set developer_instructions, append it to yours.
developer_instructions = """…"""

[mcp_servers.wenna]
command = "/full/path/to/wenna"
args = ["stdio"]
default_tools_approval_mode = "approve"
```

## Settings

Environment variables. All `wenna` processes on a machine share one database.

| Variable | Default | Used by |
|---|---|---|
| `WENNA_DB` | `$HOME/.wenna/wenna.db` (absolute path; when set, its directory must already exist) | all |
| `WENNA_TOKEN` | none: required, 32+ bytes | `serve` |
| `PORT` | `8080` | `serve` |
| `WENNA_LISTEN_HOST` | `127.0.0.1`; loopback only (`127.0.0.1`, `::1`) unless the next is `true` | `serve` |
| `WENNA_BEHIND_TLS_PROXY` | `false`; `true` only when a proxy terminates HTTPS in front of every request ([§ Choose a host](#choose-a-host)) | `serve` |
| `WENNA_TRIGRAM` | `true`: recall matches text inside a word ("foods" finds "atlasfoods.com"). The database grows to several times your memories' text and writes slow down: watch its size once memory is large. `false` saves the space | all |

## Keep your data

- **Export:** `curl -f -H "Authorization: Bearer $(cat ~/.wenna/token)" https://<your host>/api/export -o wenna-export.json.gz`
- **Revoke access:** a new token (A: a new generated value in the entry; C:
  overwrite the file), set where the server reads it, restart, then update
  every app.
- **Back up** by exporting regularly. The platform's disk is not a backup.
- **Watch disk usage** as memory grows: [§ Disk](#disk).
