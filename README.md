# lmicesync

Peer-to-peer auto-save and repository sync for the LinuxMice fleet.

Work is lost when a machine dies or a repo is never committed. `lmicesync`
snapshots dirty repositories onto `auto/<device>` branches and exchanges
those snapshots between devices over the local network. It never touches
public remotes, never commits to your real branches, and never
auto-resolves conflicts. It is a safety net, not a merge tool.

- **save** – snapshot every dirty repo under the configured roots onto
  `auto/<device>` using a temporary index. Your working tree and current
  branch are never modified.
- **serve** – LAN agent: UDP discovery beacon (port 10001) plus an HTTP
  bundle server (port 7370). Every request needs the shared bearer token;
  with `require_approval` on, only devices listed in `approval.json` may
  pull.
- **sync** – fetch peers' `auto/*` refs (as git bundles) into
  `refs/peers/<device>/auto/...` and merge them into `auto/merged`.
  Conflicting merges abort and are reported; `auto/merged` stays untouched
  so the operator can resolve manually.
- **peers** – list devices discovered on the LAN.

## Quick start

```sh
go build -o bin/lmicesync ./cmd/lmicesync
./bin/lmicesync init          # writes ~/.config/lmicesync/config.json
./bin/lmicesync save          # snapshot dirty repos now
./bin/lmicesync serve         # run the LAN agent
./bin/lmicesync sync          # pull peers and merge into auto/merged
```

Config defaults: roots `~/Documents/{github,gitlab,forgejo}`, device is the
hostname, HTTP on `:7370`, beacon UDP 10001, approval required. Every value
is overridable with `LMICESYNC_*` environment variables
(`LMICESYNC_DEVICE`, `LMICESYNC_TOKEN`, `LMICESYNC_ROOT`, ...).

Set a shared token on every fleet machine (config or `LMICESYNC_TOKEN`).
With approval required, list each other's device names in
`~/.config/lmicesync/approval.json` once; after that sync works without
intervention.

## Install

```sh
./install.sh   # binary, config, approval file, systemd timers (save 15m, sync 5m)
systemctl --user enable --now lmicesync-serve.service   # optional LAN agent
```

## Design rules

- Only `auto/*` refs ever cross the wire. Public remotes are never read or
  written by this tool.
- Snapshots land on `auto/<device>`, merges on `auto/merged`. The worktree
  and checked-out branch are never touched.
- Conflicts are surfaced, never resolved. Manual resolution wins; a later
  sync retries automatically.
- Scriptable and machine-readable: JSON config, one-line-per-event output.

## Test

```sh
go vet ./...
tests/smoke.sh    # builds A and B machines with shared history; save,
                  # serve with auth + approval gates, discovery sync,
                  # merge, and conflict surfacing
```
