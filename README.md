# Wenna

Let your personal agents talk to each other. Using more than one? Stop
copying between them: what you tell one, the others know.

Wenna is an experiment from the team at [Kinelo](https://www.kinelo.com/?ref=wenna_github),
given to the community. We play with personal agents too, and made this so ours
could share what we tell them. Free, open source, no Wenna service in between.

It is a small MCP server over one SQLite file. Every agent you connect reads
and writes the same notes: remember, update, forget, recall, list. History is
kept; you can export all of it at any time.

- **Setup guide your agent can follow:** [trywenna.com/llms.md](https://trywenna.com/llms.md).
- **Security:** for personal, low-risk use; it lacks advanced security
  features. Threat model and what is missing: [SECURITY.md](SECURITY.md).
- **Contributions:** we're not accepting pull requests at the moment.
- **Working in this repo:** [CLAUDE.md](CLAUDE.md).

## Install

macOS and Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/uni-industries/wenna/main/scripts/install.sh | bash
```

It verifies the download's checksum and installs `wenna` to `~/.local/bin`, no
sudo. `WENNA_VERSION=<tag>` picks a release; `WENNA_INSTALL_DIR` picks the
directory. Check with `wenna version`.

From source, which needs Go, make and a C compiler (SQLite is compiled in):

```sh
git clone https://github.com/uni-industries/wenna.git && cd wenna
make build OUT="$HOME/.local/bin/wenna"   # any directory on your PATH
```

## Connect your personal agents

Personal agents run in the cloud, so they reach Wenna at an HTTPS address you
host: `wenna serve` behind a token. [HOSTING.md](HOSTING.md) picks a host with
you, prices included, then connects each agent
([§ Connect your personal agents](HOSTING.md#connect-your-personal-agents)).

Run one `wenna serve` and point every agent at it. To revoke access, change
the token and restart `wenna serve`.

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
kept in a file only you can read ([HOSTING.md § The token](HOSTING.md#the-token)):

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
[HOSTING.md](HOSTING.md). A proxy on the same machine must send
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

## Export

```sh
wenna export > wenna-export.json.gz
```

Or `GET /api/export` with the token. The file holds every change to every
memory, in order.

## Settings

Environment variables. All `wenna` processes on a machine share one database.

| Variable | Default | Used by |
|---|---|---|
| `WENNA_DB` | `$HOME/.wenna/wenna.db` (absolute path; when set, its directory must already exist) | all |
| `WENNA_TOKEN` | none: required, 32+ bytes | `serve` |
| `PORT` | `8080` | `serve` |
| `WENNA_LISTEN_HOST` | `127.0.0.1`; loopback only (`127.0.0.1`, `::1`) unless the next is `true` | `serve` |
| `WENNA_BEHIND_TLS_PROXY` | `false`; `true` only when a proxy terminates HTTPS in front of every request ([HOSTING.md](HOSTING.md)) | `serve` |
| `WENNA_TRIGRAM` | `true`: recall matches text inside a word ("foods" finds "atlasfoods.com"). The database grows to several times your memories' text and writes slow down: watch its size once memory is large. `false` saves the space | all |

## License

[AGPL-3.0](LICENSE). An experiment from the team at
[Kinelo](https://www.kinelo.com/?ref=wenna_github), given to the community.
