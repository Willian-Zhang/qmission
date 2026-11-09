# testdata

Recorded qBittorrent Web API (`/api/v2`) responses, served to the tests by `gock`.

## Layout

- `*.json` at the top level: the original 2019 set (qBT 4.1 era). The current tests use it, so leave it alone until they move to the per-version sets.
- `4.6/`: qBittorrent 4.6.4 (WebAPI 2.9.3), local `container` test bed.
- `5.3/`: qBittorrent 5.3 (the NAS). Captured once `scrachpad/qbt.env` exists.

Files in each version dir:

| File | Endpoint |
| --- | --- |
| `app_version.txt`, `webapi_version.txt` | `app/version`, `app/webapiVersion` (raw text, no newline) |
| `preferences.json` | `app/preferences` |
| `sync_initial.json` | `sync/maindata?rid=0` |
| `sync_1.json`, `sync_2.json` | the next two `sync/maindata` polls, each requested with the `rid` from the previous response (so `sync_N.json` answers `rid=N`) |
| `torrent_list.json` | `torrents/info` |
| `torrent_<n>_{properties,trackers,piecestates,files,peers}.json` | `torrents/properties`, `trackers`, `pieceStates`, `files`, `sync/torrentPeers?rid=0` for up to 3 torrents: one downloading (or `metaDL`), one seeding, one paused/stopped |

`<n>` is the torrent's fake hash as a number: torrent `n` has hash `n` zero-padded to 40 hex digits.

## Anonymization

`anonymize.py` rewrites everything before it is written; the real→fake mapping is consistent within one capture.

- Hashes → `000…0<n>` (v2: 64 hex digits); names → `Torrent <n>`; files → `Torrent <n>/file <m>.<ext>` (single-file: `Torrent <n>.<ext>`).
- Paths → `/downloads`, `/downloads/dir<k>`; trackers → `<scheme>://tracker<k>.example/announce` (`** [DHT] **` and friends kept); magnet URIs rebuilt from the fake values.
- Peers → `10.99.x.y:<port>`, clients → `Client <k>`; comment/creator → fixed strings; tags/categories → `tag<k>`/`category<k>`.
- Preferences: string values with credential-, path-, host- or list-like keys are blanked, and objects/arrays emptied.
- Numbers, booleans and states are never changed. A final pass replaces any leftover 40/64-hex string, URL or IPv4 anywhere.

## Regenerate

```sh
# from the repo root
sh cmd/qmission/testdata/test_data_fetcher.sh scrachpad/qbt.env   QBT53 cmd/qmission/testdata/5.3
sh cmd/qmission/testdata/test_data_fetcher.sh scrachpad/qbt46.env QBT46 cmd/qmission/testdata/4.6
sh cmd/qmission/testdata/leak_check.sh cmd/qmission/testdata/5.3   # counts only, exit 1 on a leak
```

The env file defines `<PREFIX>_URL` (with trailing slash), `<PREFIX>_USER` and `<PREFIX>_PASS`. The script sources it and never prints it. Raw responses stay in a private temp dir that is deleted on exit. Needs `curl`, `jq` and `python3`. A run overwrites the files in the output dir but doesn't delete old `torrent_<n>_*` files, so clear the dir first if the torrent mix changed.
