# qmission

qmission (qBittorrent + Transmission) allows to use Transmission remote controls with qBittorrent.
It acts as a bridge between Transmission RPC and qBittorrent WebUI API.

qmission is a fork of [Reflection](https://github.com/h31/Reflection) by Artyom Aleksyuk (MIT).

Currently qmission is able to:
* Show torrent list
* Show torrent details (files, trackers, statistics)
* Start, stop, delete torrents
* Add new torrents from a file, a magnet link of an HTTP/HTTPS link
* Choose which files should be downloaded
* Change destination directory
* Show peer table

## Installation

Requires Go 1.23+.

```
go install github.com/Willian-Zhang/qmission/cmd/qmission@latest
```

On Linux, to run it as a systemd service, copy the binary to `/usr/local/bin/qmission` and install [qmission.service](qmission.service):

```
sudo cp qmission.service /etc/systemd/system/
sudo systemctl enable --now qmission.service
```

## Docker

The [Dockerfile](Dockerfile) builds a small Alpine image (non-root, binary at `/usr/local/bin/qmission`, port 9091) for `linux/amd64` and `linux/arm64`:

```
docker buildx build --platform linux/amd64,linux/arm64 -t qmission .
```

[compose.yaml](compose.yaml) runs it from a checkout (`build: .`, image `qmission:local`). Copy [.env.example](.env.example) to `.env` and fill in:

* `QBT_USER`, `QBT_PASS`: the qBittorrent WebUI login.
* `QMISSION_USER`, `QMISSION_PASS`: the Basic-auth credentials clients use for the bridge (required).

Edit the `-r` qBittorrent URL, the network and the IP address to match your setup. The healthcheck polls `/healthz`, which returns 200 while the qBittorrent login works.

## CI

[GitHub Actions](.github/workflows/ci.yml) builds, vets and tests (`go test -race`) on every push to `master` and every pull request, and builds the multi-arch image without pushing it.

## Configuration

* `QBT_USER`, `QBT_PASS` (env): the qBittorrent WebUI login. `-qbt-user`/`-qbt-pass` override them, but flags show up in the process list, so prefer the env vars.
* `QMISSION_USER`, `QMISSION_PASS` (env, required): the Basic-auth credentials clients must send. qmission refuses to start without them; pass-through of client credentials to qBittorrent isn't implemented yet. Client credentials are never forwarded to qBittorrent.
* qmission logs in to qBittorrent at startup and keeps retrying in the background while qBittorrent is unreachable; a qBittorrent restart is picked up by logging in again.
* `/healthz` (no auth) answers `200 ok` while the qBittorrent login and an API call work, `503` otherwise.
* `-d` logs full RPC bodies, which contain torrent names, paths and hashes. Don't share such logs.

## Compatibility
* qmission speaks the classic Transmission RPC protocol (the `X-Transmission-Session-Id` handshake, camelCase/kebab-case names) and reports `rpc-version` 18, i.e. Transmission 4.1.0 (`version` is e.g. `4.1.0 (qmission, qBT 5.3.0)`). JSON-RPC 2.0 isn't supported yet.
* Requires at least qBittorrent 4.1.0. Tested on 4.6.x and 5.3.x; the qBittorrent version is detected at login, so 4.x (`pause`/`resume`) and 5.x (`stop`/`start`) both work.

Stage 1a caveats (read-only use plus list polling):
* Methods and writes that aren't implemented yet (`session-set`, `torrent-set` except `files-wanted`/`files-unwanted`, `torrent-start-now`, `torrent-reannounce`, `torrent-rename-path`, `queue-*`, `free-space`, `port-test`, `blocklist-update`, `group-*`) fail with `not implemented in qmission yet` instead of pretending to succeed.
* Torrent IDs start at 1 and are stable only while qmission runs; they're reassigned after a restart.
* Speeds and limits are in KiB/s (`units.speed-bytes` 1024). qBittorrent's `0` (unlimited) shows as a disabled limit.
* List polls are served from qBittorrent's torrent list without per-torrent requests. `trackerStats` there holds one entry for qBittorrent's current tracker (with the swarm's seed/peer counts); the real tracker list comes with a single-torrent request (the details pane). `pieceCount`, `pieceSize`, `dateCreated`, `isPrivate` and `file-count` are read once per torrent and cached; `pieceCount` is 0 while qBittorrent is still fetching a magnet's metadata. Fields that need `torrents/properties` (`corruptEver`, `peer-limit`, `comment`, ...) are only filled for single-torrent requests.
* `queuePosition`: queued torrents first in qBittorrent's queue order, then the torrents outside qBittorrent's queue (e.g. seeding) by ID.
* `download-dir-free-space` is the free space of qBittorrent's default save path.
* A torrent in qBittorrent's `error`/`missingFiles` state shows as stopped with a local error; qBittorrent's API gives no error text.
* Tested against Transmission Remote GUI, built-in Transmission Web UI, Torrnado client for Android, Transmission-Qt and Transmission Remote by Yury Polek. Please fill an issue if you experience an incompatibility with any client.

What features are not supported yet:
* Setting torrent properties (download/upload speed, etc)
* Showing and changing torrent client settings

## qBittorrent and Transmission-specific options

Please note that both qBittorrent and Transmission have some unique features.
For example, some torrent properties such as a private flag are not exposed by qBittorrent.
In case Transmission clients request such information, qmission responds with predefined template data. Template values are stored in [transmission/templates.go](transmission/templates.go).

To enable some qBittorrent-specific options, use a "Specify torrent location" command in your Transmission GUI
and append a special flag to the path. You can also specify such paths when adding a new torrent.
For example, if download directory is `/home/user/`, those paths can be used:
* `/home/user/+s` to enable sequential download
* `/home/user/+f` to download first and last pieces first 
* `/home/user/+h` to skip hash checking when adding torrent.

It is possible to combine several commands, i.e. `/home/user/+sf`. Use `-` sign instead of `+` to disable an option.
If your want to disable command processing and treat a path just as a path, end it with `/`, i.e. `/home/user/my+path+s/`.

qmission applies some optimizations to make things smoother:
* Both qBittorent and Transmission have an ability to transmit not the whole torrents list but just the recently changed information.
They do it in a slightly different way, but qmission tries to compensate differences.
Use `-sync=false` command line flag to disable this optimization and make qmission request the whole torrents list each time.
* Due to the way the qBittorrent's API was designed, some requests can be quite slow.
qmission answers list polls from the sync data alone and caches the per-torrent values that never change (piece count and size), so polling stays cheap. `-cache-timeout` is kept for compatibility and has no effect.

## Usage:

```bash
go run ./cmd/qmission    # from a checkout
go test ./...
```

Use a `--help` flag to show settings. Default qBittorrent address is `http://localhost:8080/`.
