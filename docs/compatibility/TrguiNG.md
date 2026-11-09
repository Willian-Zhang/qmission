# TrguiNG

[TrguiNG](https://github.com/openscopeproject/TrguiNG), desktop and web builds. Primary target of qmission; tested against qBittorrent 4.6.4 and 5.3.0.

## Setup

In TrguiNG, add a server with your qmission host, port `9091`, path `/transmission/rpc`, and the `QMISSION_USER` / `QMISSION_PASS` login. TrguiNG sees rpc-version 18, so every feature is enabled.

## What works

| Area | Function | Status |
|---|---|---|
| Connection | Login, version, status bar (speeds, limits, free space) | ✅ |
| List | All columns, filters (status, labels, trackers, folders), search, copy magnet link | ✅ |
| Actions | Start, Pause, Force start, Verify, Reannounce, Remove (with or without data) | ✅ |
| Queue | Move up / down / to top / to bottom, Queue position column | ✅ needs queueing turned on in qBittorrent |
| Add | Magnet, .torrent file, URL; download folder, labels, Start torrent, Sequential, file selection, torrent name | ✅ Priority must stay Normal |
| Details | General, Files, Pieces, Peers, Trackers, Server statistics | ✅ |
| Files tab | Wanted checkbox, High / Normal / Low priority, rename file or folder | ✅ |
| Rename | Torrent name (inline edit) | ✅ renames the top folder or file on disk too |
| Properties | Download / upload limits, seed ratio, stop seeding when inactive, sequential | ✅ "inactive" needs qBittorrent 4.6+ |
| Labels | Set labels, one or several torrents | ✅ |
| Trackers | Edit trackers; "Add trackers" for a duplicate in the Add dialog | ✅ adding and removing URLs |
| Move | With "Move torrent data" ticked | ✅ |
| Server settings | Download, Network, Bandwidth, Queue tabs | ✅ except the options below |
| Alt speed | Toolbar speedometer button, "Use alternate bandwidth", schedule | ✅ |
| Port test | "Test port" button | ≈ "open" = qBittorrent reports itself connected |

## Don't use

qBittorrent has nowhere to store these. Changing one shows an error and nothing in that save is applied; leaving them unchanged is fine.

| Where | Option | What you'll see |
|---|---|---|
| Toolbar → Set priority (Ctrl+H / Ctrl+L) | High or Low | "Failed to update priority" (Normal does nothing) |
| Add dialog | Priority Low / High | the add fails; use Normal |
| Properties | Peer limit | "Error updating torrent" |
| Properties | Unticking "Honor session upload limit" | "Error updating torrent" |
| Server settings → Download | Download sequentially (global default) | error |
| Server settings → Network | Blocklist URL; "Update" button | URL change fails; Update reports 0. The on/off switch works |
| Server settings → Queue | Download and seed queue switched on/off differently | error (qBittorrent has one switch) |
| Server settings → Bandwidth | Schedule days other than every day / weekdays / weekends / one day | error |
| Server settings → Network | Random port together with a changed port number | error |
| Move dialog | "Move torrent data" unticked | error (qBittorrent always moves the data) |
| Trackers | Moving trackers between tiers, inserting a tier between existing ones | error |
| Bandwidth group | column and General field | always empty |

TrguiNG shows only "Error updating torrent" for Properties; the exact reason is in qmission's log (warn level).

## Differences

- Files at qBittorrent's default priority show as **Low**; setting Normal stores qBittorrent "High", High stores "Maximum".
- A speed limit value edited while its checkbox is off is dropped.
- Server statistics: "Files added" and "Session count" are 0.
- Encryption "Tolerated" = qBittorrent "disable encryption".
- Torrent IDs change after a qmission restart (as with Transmission); TrguiNG reloads the list.
- In the Add dialog, file selection only exists for .torrent files (desktop build), which is also the only case qmission supports.
- Adding by URL: a URL that fails to download only shows a timeout error after 10 s.
- TrguiNG's F8 hotkey doesn't toggle alternative speed (a TrguiNG issue); use the toolbar button.
