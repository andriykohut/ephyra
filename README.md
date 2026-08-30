# Ephyra

A stats dashboard for Jellyfin. It reads copies of Jellyfin's SQLite files on a
schedule, rolls them up into its own small database, and serves the result as a
single Go binary with the frontend baked in. Standing load on Jellyfin is
basically nil.

Named after the juvenile stage of a jellyfin— sorry, jellyfish.

**This build ships the Library page.** Watch Stats, Now Playing, and Cleanup are
stubs for now.

## What you need

- Jellyfin 10.11, running on the same Docker host.
- The ability to bind-mount Jellyfin's config directory into another container,
  read-only.
- A Jellyfin API key (Dashboard → API Keys → +). Only used for the live bits
  that aren't in this build yet; still required so the config is complete.

## Run it

```yaml
# docker-compose.yml
services:
  ephyra:
    image: ghcr.io/OWNER/ephyra:latest
    environment:
      JELLYFIN_URL: http://jellyfin:8096
      JELLYFIN_API_KEY: ${EPHYRA_JELLYFIN_API_KEY}
      JELLYFIN_DATA_DIR: /jellyfin-data
    volumes:
      - /path/to/jellyfin/config:/jellyfin-data:ro
      - ephyra-data:/data
    ports:
      - "8080:8080"
    restart: unless-stopped
volumes:
  ephyra-data:
```

`docker compose up -d`, then open `http://<host>:8080`. The first refresh runs on
startup; the Library page fills in a second or two later.

The container runs as UID 65532. A fresh named volume for `/data` picks that up
automatically; if you bind-mount a host directory there instead, `chown 65532
<dir>` first.

No auth. Put it on your LAN, or behind whatever your reverse proxy already does
for login.

## Configuration

| Variable | Required | Default | Notes |
|---|---|---|---|
| `JELLYFIN_URL` | yes | — | e.g. `http://jellyfin:8096` |
| `JELLYFIN_API_KEY` | yes | — | not exercised in this build, still required |
| `JELLYFIN_DATA_DIR` | yes (unless `SOURCE=api`) | — | the read-only mount; DBs read from `<dir>/data/` |
| `SOURCE` | no | `auto` | `file` \| `api` \| `auto`. `api` is not implemented yet |
| `STORE_PATH` | no | `/data/ephyra.db` | Ephyra's own database |
| `WORK_DIR` | no | `/data/work` | scratch space for DB copies; must be writable |
| `LISTEN_ADDR` | no | `:8080` | |
| `REFRESH_LIBRARY` | no | `30m` | how often to re-read the library |
| `REFRESH_WATCH` | no | `10m` | unused in this build |
| `LIVE_POLL_INTERVAL` | no | `4s` | unused in this build |
| `DIRECT_READ` | no | `false` | skip the copy, read the live DB with `immutable=1`. Only if your mount is read-write |
| `STREAM_CAPACITY` | no | unset | unused in this build |
| `LOG_LEVEL` | no | `info` | `debug` \| `info` \| `warn` \| `error`; JSON to stdout |
| `TZ` | no | `UTC` | affects month bucketing on the growth chart |

## How it reads data

The Jellyfin mount is read-only, and SQLite won't open a WAL database read-only
without writing a `-shm` sidecar. So each refresh copies `library.db` (plus its
`-wal` / `-shm`) into `WORK_DIR`, queries the copy, and deletes it. If the file's
mtime hasn't changed since the last successful run, the refresh is skipped
entirely — libraries don't change often, so most runs do no work.

Ephyra never writes to Jellyfin or to the mounted directory.

## Verify the schema (once, recommended)

Ephyra's queries assume a particular `library.db` layout (see
`docs/schema-notes.md`). Jellyfin builds vary a little. On the Jellyfin host:

```sh
JELLYFIN_DATA_DIR=/path/to/jellyfin/config ./scripts/dump-jellyfin-schema.sh
```

Compare the dump against `docs/schema-notes.md`. If something's off, the numbers
on the Library page will look wrong — the notes say which queries to adjust.

## Development

Two processes:

```sh
cd web && npm run dev      # :5173, proxies /api and /healthz to :8080
go run ./cmd/ephyra        # :8080
```

Point `JELLYFIN_DATA_DIR` at a directory with a `data/library.db`. There's a
throwaway fixture at `testdata/library.fixture.sql` if you don't want to touch a
real one:

```sh
mkdir -p /tmp/jf/data && sqlite3 /tmp/jf/data/library.db < testdata/library.fixture.sql
JELLYFIN_URL=x JELLYFIN_API_KEY=x JELLYFIN_DATA_DIR=/tmp/jf \
  STORE_PATH=/tmp/ephyra.db WORK_DIR=/tmp/ephyra-work go run ./cmd/ephyra
```

`make test` runs the Go tests. `make build` builds the frontend then the binary.
`make docker` builds the image.

## Design docs

`docs/superpowers/specs/` has the design spec; `docs/superpowers/plans/` has the
implementation plans. This is Plan 1 of 3.
