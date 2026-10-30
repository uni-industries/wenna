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

- **Goals and checks, not click paths.** Vendor dashboards change; find the
  current menu yourself. Each section gives what must be true and how to
  check it.
- **Ask, don't assume:** which apps, which of them run on this computer
  (`wenna stdio`), whether the machine stays on, whether the user owns a
  domain, which path. Name every cost before the user commits; never accept
  billing or upgrade a plan yourself.
- **The user does only the steps each path lists** (sign-up, billing,
  browser approvals, dashboard-only switches). You do the rest by CLI or API.
  Check each path's tools first; install what is missing. On your own
  machine, ask whether it meets [the keep-serving goals](#your-own-machine);
  the user sets them.
- **The user does the secret and personal parts:** sign-ins, emailed codes,
  captchas. A vendor API key the user creates goes straight to a file in
  `~/.wenna`, mode 600, never through the chat. Ask them to run commands with
  `! …` when a secret must be typed.
- **Never print the token** or put it on a command line; feed it from its
  file ([§ The token](#the-token)). When an app needs the user to paste it,
  you check it and put it on their clipboard
  ([§ Give it to the user](#give-it-to-the-user)).
- **Verify each step before the next.** Done when, for the hosted URL:

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

### The token

Every path keeps it in one file the user owns, on the computer you set up
from, and feeds the platform and apps from that file.

```sh
mkdir -p -m 700 ~/.wenna
[ -e ~/.wenna/token ] || (umask 077 && openssl rand -hex 32 > ~/.wenna/token)
```

An existing file is in use: keep it. Overwriting it is rotation
([§ Keep your data](#keep-your-data)).

| Into | From the file |
|---|---|
| A command's input | `< ~/.wenna/token`, or `{ printf 'WENNA_TOKEN='; cat ~/.wenna/token; } \|` for `NAME=value` input |
| A JSON body or file | `jq -n --rawfile t ~/.wenna/token '{… ($t\|rtrimstr("\n")) …}'` |
| A command's argument | `"$(cat ~/.wenna/token)"`, only where nothing else works (it shows in the process list) |
| A web form or an app's chat | The user pastes it from the clipboard: [§ Give it to the user](#give-it-to-the-user) |

Apps on another computer need the token there too; the user copies the file
over, never through a chat.

#### Give it to the user

For any app that needs the token typed into its own screen. The user never
looks for it, and you never see it.

1. **Check it is this host's.** Never ask the user for it; it is in
   `~/.wenna/token`.

   ```sh
   { printf 'Authorization: Bearer '; cat ~/.wenna/token; } |
     curl -s -o /dev/null -w '%{http_code}\n' -H @- https://<host>/api/export   # 200
   ```

   No file, or 401: this computer does not hold this host's token. Ask the
   user which computer set the host up; copy the file from there. Never
   create a new one; that is rotation.
2. **Copy it**, without the file's trailing newline. On a Mac:
   `tr -d '\n' < ~/.wenna/token | pbcopy`. Elsewhere the user opens the file
   and copies it.
3. **Tell the user** the token is on their clipboard, and exactly where to
   paste it (the app's field name). Wait for them to confirm.
4. **Clear the clipboard:** `pbcopy < /dev/null`.

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

Alternative: New → Blueprint in the dashboard, from [render.yaml](render.yaml);
it needs the Git connection and prompts once for `WENNA_TOKEN`, which the user
pastes. Every push to the linked branch then redeploys.

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
and pastes the token there themselves
([§ Give it to the user](#give-it-to-the-user)); the agent never does. With
Cloudflare Access on, give them the bearer hostname, not the Access one. The token then sits in that app's chat history;
rotating it is the way back. Check: the app recalls a test memory another app
saved.

### Muse

Set up in its chat, but the token goes into a form, not the chat.

1. The user pastes this into a Muse chat; you fill in `<host>` (the bearer
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
   connector notice and continues. You put the token on their clipboard
   ([§ Give it to the user](#give-it-to-the-user)); they paste it into the
   **API key** field as is (no `Bearer `), then **Add**.
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

1. The user pastes this into an Instinct chat; you fill in `<host>` (the
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
- **Revoke access:** overwrite the token file, set the new token where the
  server reads it, restart, then update every app.
- **Back up** by exporting regularly. The platform's disk is not a backup.
- **Watch disk usage** as memory grows: [§ Disk](#disk).
