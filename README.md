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

## Export

```sh
wenna export > wenna-export.json.gz
```

Or `GET /api/export` with the token. The file holds every change to every
memory, in order.

## Settings

Environment variables: [HOSTING.md § Settings](HOSTING.md#settings).

## License

[AGPL-3.0](LICENSE). An experiment from the team at
[Kinelo](https://www.kinelo.com/?ref=wenna_github), given to the community.
