# qmission

Use **Transmission apps with qBittorrent**.

qmission is a small bridge: it speaks the Transmission RPC protocol to your app (e.g. [TrguiNG](https://github.com/openscopeproject/TrguiNG)) and translates every request to qBittorrent's WebUI API.

```
TrguiNG / other Transmission app  ──Transmission RPC──▶  qmission  ──WebUI API──▶  qBittorrent
```

qmission is a fork of [Reflection](https://github.com/h31/Reflection) by Artyom Aleksyuk (MIT).

## Features

- [x] Torrent list with filters, search and live updates
- [x] Details: files, peers, trackers, pieces, server statistics
- [x] Add torrents: magnet link, .torrent file, URL, with download folder, labels, sequential download and file selection
- [x] Start, stop, force start, verify, reannounce, remove (with or without data)
- [x] Queue, move, rename (torrents, files and folders)
- [x] Torrent properties: speed and seeding limits, labels, trackers, file priorities
- [x] Server settings: speed limits, alternative speed and its schedule, queue, network
- [x] qBittorrent 4.x and 5.x, detected automatically
- [x] Docker image (amd64, arm64)

Some Transmission options have no equivalent in qBittorrent. Changing one gives a clear error and changes nothing. Per-app details: [docs/compatibility](docs/compatibility/README.md).

## Roadmap

- [ ] Prebuilt Docker image and release binaries
- [ ] Test and document more apps: Transmission Web UI, Transmission Remote GUI, Transdroid, Sonarr/Radarr, ... ([docs/compatibility](docs/compatibility/README.md))
- [ ] Transmission 4.1's newer JSON-RPC 2.0 protocol
- [ ] Use the app's own login for qBittorrent (no separate qmission login)
- [ ] Unwanted files when adding a magnet link (applied once its metadata arrives)
- [ ] Serve TrguiNG's web interface from qmission

Requirements: qBittorrent **4.1 or newer** (tested on 4.6 and 5.3) with its WebUI turned on.

## Quick start (Docker)

The [example stack](examples/compose.yaml) runs qBittorrent and qmission together:

```yaml
# qBittorrent + qmission. Fill in .env next to this file first (see the README).
services:
  qbittorrent:
    image: lscr.io/linuxserver/qbittorrent:latest
    environment:
      PUID: 1000          # owner of the downloaded files: your user's `id -u`
      PGID: 1000          # and `id -g`
      TZ: Etc/UTC         # your time zone, e.g. Europe/Berlin
    ports:
      - "8080:8080"       # qBittorrent WebUI
      - "6881:6881"       # BitTorrent
      - "6881:6881/udp"
    volumes:
      - ./config:/config
      - ./downloads:/downloads
    restart: unless-stopped

  qmission:
    build: ..             # builds from this checkout
    # qBittorrent's service name and its port inside the container
    # (a remapped host port fails qBittorrent's Host header check).
    command: ["-r", "http://qbittorrent:8080/"]
    environment:
      QBT_USER: ${QBT_USER}             # qBittorrent WebUI login
      QBT_PASS: ${QBT_PASS}
      QMISSION_USER: ${QMISSION_USER}   # login your Transmission app uses (required)
      QMISSION_PASS: ${QMISSION_PASS}
    ports:
      - "9091:9091"       # Transmission RPC: http://<host>:9091/transmission/rpc
    # Optional: mark the container unhealthy while qmission can't log in to qBittorrent.
    # healthcheck:
    #   test: ["CMD", "wget", "-q", "-O", "/dev/null", "http://127.0.0.1:9091/healthz"]
    #   interval: 60s
    #   timeout: 10s
    #   retries: 3
    restart: unless-stopped
```

The `${...}` values come from `.env` next to the file (step 3).

1. Get the code and create the settings file:

   ```sh
   git clone https://github.com/Willian-Zhang/qmission.git
   cd qmission/examples
   cp ../.env.example .env
   ```

2. Start qBittorrent first and set its WebUI password:

   ```sh
   docker compose up -d qbittorrent
   docker compose logs qbittorrent   # shows a temporary password for user "admin"
   ```

   Open `http://<host>:8080`, log in, and set a permanent password under *Tools → Options → WebUI*.

3. Fill in `.env`:

   | Variable | Meaning |
   |---|---|
   | `QBT_USER`, `QBT_PASS` | the qBittorrent WebUI login from step 2 |
   | `QMISSION_USER`, `QMISSION_PASS` | a new login your Transmission app will use for qmission (required) |

4. Start qmission:

   ```sh
   docker compose up -d
   ```

5. In your Transmission app, add a server:

   | Setting | Value |
   |---|---|
   | Host | your Docker host |
   | Port | `9091` |
   | Path | `/transmission/rpc` |
   | Username / password | `QMISSION_USER` / `QMISSION_PASS` |

Already running qBittorrent? Use only the `qmission` service from the example and point `-r` at your qBittorrent WebUI.

## Without Docker

Requires Go 1.23+.

```sh
go install github.com/Willian-Zhang/qmission/cmd/qmission@latest
```

Put the four logins in a file (e.g. `qmission.env`, `chmod 600`), so the passwords don't end up in your shell history:

```sh
QBT_USER=admin
QBT_PASS=...
QMISSION_USER=me
QMISSION_PASS=...
```

Then run:

```sh
set -a; . ./qmission.env; set +a
qmission -r http://localhost:8080/
```

For a systemd service, see the install steps at the top of [qmission.service](qmission.service).

## Options

| Option | Default | Meaning |
|---|---|---|
| `-r`, `-api-addr` | `http://localhost:8080/` | qBittorrent WebUI address |
| `-p`, `-port` | `9091` | port qmission listens on |
| `-v` | off | log one line per request |
| `-d` | off | debug log, **includes torrent names, paths and hashes**; don't share it |
| `-sync` | `true` | use qBittorrent's incremental updates (faster) |
| `-disable-keep-alive` | off | for old qBittorrent versions with connection problems |
| `QBT_USER`, `QBT_PASS` | | qBittorrent login (env; `-qbt-user`/`-qbt-pass` also work but show up in `ps`) |
| `QMISSION_USER`, `QMISSION_PASS` | | login for Transmission apps (env, required) |

`GET /healthz` (no login) returns `200 ok` while qmission can reach and log in to qBittorrent, `503` otherwise.

## Troubleshooting

- **qmission can't log in to qBittorrent (401):** qBittorrent checks the `Host` header. Use the address and port qBittorrent itself listens on: in Docker that's the service name and the *container* port (`http://qbittorrent:8080/`), not a remapped host port. Behind a reverse proxy, add its hostname under *Options → WebUI → Server domains*, or turn off Host header validation.
- **"banned" errors:** qBittorrent blocks an IP for an hour after 5 failed logins. Fix `QBT_USER`/`QBT_PASS`, then restart qBittorrent or wait.
- **qmission refuses to start:** set `QMISSION_USER` and `QMISSION_PASS`.
- **A setting fails with "qBittorrent can't store these fields":** that option doesn't exist in qBittorrent. Leave it unchanged; see [docs/compatibility](docs/compatibility/README.md).

## Good to know

- Labels are qBittorrent **tags**.
- Speeds are in KiB/s.
- Torrent IDs are renumbered when qmission restarts (Transmission does the same).
- Free space always refers to qBittorrent's default download folder.
- Moving a torrent always moves its data.
- Classic Transmission RPC only (rpc-version 18, reported as `4.1.0 (qmission, qBT <version>)`); Transmission's newer JSON-RPC 2.0 isn't supported yet.

## Development

```sh
go build ./cmd/qmission
go test ./...
```

The tests replay recorded, anonymized qBittorrent 4.6 and 5.3 responses ([cmd/qmission/testdata](cmd/qmission/testdata/README.md)). CI ([GitHub Actions](.github/workflows/ci.yml)) builds, vets, tests and builds the multi-arch Docker image.

## License

MIT, see [LICENSE.txt](LICENSE.txt).
