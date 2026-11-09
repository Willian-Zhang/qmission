# Compatibility

What works through qmission, per Transmission app. The behaviour common to all apps is below; each app's page lists what it actually uses.

| App | Status | Page |
|---|---|---|
| TrguiNG (desktop and web) | supported, primary target | [TrguiNG.md](TrguiNG.md) |
| Transmission Web UI | not checked yet | [transmission-web.md](transmission-web.md) |
| Transmission Remote GUI (transgui) | not checked yet | [transgui.md](transgui.md) |
| Transmission-Qt (remote mode) | not checked yet | [transmission-qt.md](transmission-qt.md) |
| Transdroid / Transdrone (Android) | not checked yet | [transdroid.md](transdroid.md) |
| Torrnado (Android) | not checked yet | [torrnado.md](torrnado.md) |
| Transmission Remote by Yury Polek (Android) | not checked yet | [transmission-remote-android.md](transmission-remote-android.md) |
| Sonarr / Radarr (Transmission download client) | not checked yet | [sonarr-radarr.md](sonarr-radarr.md) |
| clutch (Python library) | not checked yet | [clutch.md](clutch.md) |

Upstream Reflection was tested with Transmission Remote GUI, the Transmission Web UI, Torrnado, Transmission-Qt and Transmission Remote (Yury Polek). qmission changed a lot since, so those are listed as not checked.

## Protocol

- Classic Transmission RPC: Basic auth plus the `X-Transmission-Session-Id` handshake, at `/transmission/rpc` (also `/rpc`).
- Reports `rpc-version` 18, `rpc-version-semver` `5.3.0`, `version` `4.1.0 (qmission, qBT <version>)`.
- Not supported yet: JSON-RPC 2.0 / snake_case (Transmission 4.1's new protocol), passing the app's login through to qBittorrent.

## Methods

| Method | Status | Notes |
|---|---|---|
| `session-get`, `session-stats` | ✅ | `filesAdded`, `sessionCount` are 0 |
| `session-set` | ✅ | see "Fields qBittorrent can't store" |
| `torrent-get` | ✅ | fields qBittorrent has no value for return neutral defaults |
| `torrent-set` | ✅ | see "Fields qBittorrent can't store" |
| `torrent-add` | ✅ | magnet (hex, base32, v2), URL, `metainfo` |
| `torrent-start`, `-stop`, `-start-now`, `-verify`, `-reannounce`, `-remove` | ✅ | `start-now` = qBittorrent force start |
| `torrent-set-location` | ✅ | `move: true` only |
| `torrent-rename-path` | ✅ | files, folders and the torrent itself (renames on disk, like Transmission) |
| `queue-move-top/up/down/bottom` | ✅ | needs queueing turned on in qBittorrent |
| `free-space` | ✅ | always qBittorrent's default download folder |
| `port-test` | ≈ | "open" = qBittorrent reports itself connected |
| `blocklist-update` | stub | returns size 0 |
| `group-get`, `group-set` | stub | no bandwidth groups in qBittorrent |
| anything else | ❌ | `not implemented in qmission yet` |

## Fields qBittorrent can't store

Apps often send a whole form back, so unchanged values are always fine. Changing one of these fails the request and **nothing** in it is applied; the error names the fields.

| Where | Field |
|---|---|
| `torrent-set` | `peer-limit`, `bandwidthPriority`, `honorsSessionLimits`, `group`; `seedIdleLimit`/`seedIdleMode` before qBittorrent 4.6; `queuePosition` for several torrents or a torrent outside qBittorrent's queue; `trackerList` changes that move trackers between tiers |
| `torrent-add` | `bandwidthPriority` other than 0 (Normal); `files-unwanted` with a magnet or URL (no file list yet) |
| `session-set` | `blocklist-url`, `sequential_download`, `download-queue-enabled` ≠ `seed-queue-enabled`, `alt-speed-time-day` other than every day / weekdays / weekends / one day, a port change while `peer-port-random-on-start` is on, enabling a speed limit at 0 |
| `torrent-set-location` | `move: false` (unless nothing would move) |

## Behaves differently from Transmission

- **Labels** are qBittorrent tags; a label can't contain a comma or start with `qmission-add-`.
- **File priorities**: qBittorrent's default file priority shows as Low; Normal is stored as qBittorrent "High", High as "Maximum".
- **Speed and seeding limits**: a value edited while its limit is off is dropped (qBittorrent has nowhere to keep it).
- **Encryption** "tolerated" is qBittorrent's "disable encryption".
- **Torrent IDs** are renumbered when qmission restarts (Transmission does the same).
- **Adding by URL**: qBittorrent downloads the file; qmission finds the torrent with a temporary `qmission-add-*` tag. A URL that fails to download, or a duplicate, ends in a timeout error after 10 s.
- **Adding** waits up to 10 s for qBittorrent; a torrent that's already there is reported as `torrent-duplicate`.
- **Moving** a torrent turns off qBittorrent's automatic torrent management for it. With a separate folder for incomplete downloads, the data moves when the download completes.
- **Errors**: a torrent in qBittorrent's `error`/`missingFiles` state shows as stopped with a local error, without an error text.
- **Path flags** (`+s`/`+f`/`+h` at the end of a download folder, from Reflection) are gone; use the sequential download option.
