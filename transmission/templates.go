package transmission

type JsonMap map[string]interface{}

const (
	// RPCVersion 18 = Transmission 4.1.0 (rpc-spec.md changelog). qmission speaks the
	// old bespoke protocol only, so it reports the 4.0 semver (see scrachpad/plan.md).
	RPCVersion       = 18
	RPCVersionMin    = 14 // RpcVersionMin in transmission 4.1.4 libtransmission/rpcimpl.cc
	RPCVersionSemver = "5.3.0"
	// Version is the Transmission release matching RPCVersion.
	Version = "4.1.0"
)

// Units: qmission reports speeds and limits in KiB (speed-bytes 1024), like qBT's UI.
// TrguiNG ignores `units` and always multiplies limits by 1024.
func Units() JsonMap {
	return JsonMap{
		"memory-bytes": 1024,
		"memory-units": []string{"KiB", "MiB", "GiB", "TiB"},
		"size-bytes":   1000,
		"size-units":   []string{"kB", "MB", "GB", "TB"},
		"speed-bytes":  1024,
		"speed-units":  []string{"KiB/s", "MiB/s", "GiB/s", "TiB/s"},
	}
}

// SessionGetBase holds neutral values for the session-get keys qmission can't map.
// The mapped keys are filled from qBT preferences on every call.
var SessionGetBase = JsonMap{
	"alt-speed-down":                       0,
	"alt-speed-enabled":                    false,
	"alt-speed-time-begin":                 0,
	"alt-speed-time-day":                   127,
	"alt-speed-time-enabled":               false,
	"alt-speed-time-end":                   0,
	"alt-speed-up":                         0,
	"blocklist-enabled":                    false,
	"blocklist-size":                       0,
	"blocklist-url":                        "",
	"cache-size-mb":                        0,
	"config-dir":                           "",
	"default-trackers":                     "",
	"dht-enabled":                          false,
	"download-dir":                         "",
	"download-dir-free-space":              0,
	"download-queue-enabled":               false,
	"download-queue-size":                  0,
	"encryption":                           "preferred",
	"idle-seeding-limit":                   0,
	"idle-seeding-limit-enabled":           false,
	"incomplete-dir":                       "",
	"incomplete-dir-enabled":               false,
	"lpd-enabled":                          false,
	"peer-limit-global":                    0,
	"peer-limit-per-torrent":               0,
	"peer-port":                            0,
	"peer-port-random-on-start":            false,
	"pex-enabled":                          false,
	"port-forwarding-enabled":              false,
	"queue-stalled-enabled":                false,
	"queue-stalled-minutes":                0,
	"rename-partial-files":                 false,
	"rpc-version":                          RPCVersion,
	"rpc-version-minimum":                  RPCVersionMin,
	"rpc-version-semver":                   RPCVersionSemver,
	"script-torrent-added-enabled":         false,
	"script-torrent-added-filename":        "",
	"script-torrent-done-enabled":          false,
	"script-torrent-done-filename":         "",
	"script-torrent-done-seeding-enabled":  false,
	"script-torrent-done-seeding-filename": "",
	"seed-queue-enabled":                   false,
	"seed-queue-size":                      0,
	"seedRatioLimit":                       0,
	"seedRatioLimited":                     false,
	"sequential_download":                  false,
	"speed-limit-down":                     0,
	"speed-limit-down-enabled":             false,
	"speed-limit-up":                       0,
	"speed-limit-up-enabled":               false,
	"start-added-torrents":                 true,
	"trash-original-torrent-files":         false,
	"utp-enabled":                          true,
	"version":                              Version + " (qmission)",
}

// TorrentGetDefaults has a neutral value for every torrent-get field of the
// bespoke protocol (rpc-spec.md 4.1.4 §3.3, old names: camelCase, except
// `file-count`, `peer-limit`, `primary-mime-type` and the snake_case
// `sequential_download*`). Fields qmission can't map are answered from here.
// Slices are fresh per call so callers may modify them.
func TorrentGetDefaults() JsonMap {
	return JsonMap{
		"activityDate":                   0,
		"addedDate":                      0,
		"availability":                   []int{},
		"bandwidthPriority":              0,
		"bytesCompleted":                 []int64{},
		"comment":                        "",
		"corruptEver":                    0,
		"creator":                        "",
		"dateCreated":                    0,
		"desiredAvailable":               0,
		"doneDate":                       0,
		"downloadDir":                    "",
		"downloadedEver":                 0,
		"downloadLimit":                  0,
		"downloadLimited":                false,
		"editDate":                       0,
		"error":                          0,
		"errorString":                    "",
		"eta":                            -1, // TR_ETA_NOT_AVAIL
		"etaIdle":                        -1,
		"file-count":                     0,
		"files":                          []JsonMap{},
		"fileStats":                      []JsonMap{},
		"group":                          "",
		"hashString":                     "",
		"haveUnchecked":                  0,
		"haveValid":                      0,
		"honorsSessionLimits":            true,
		"id":                             0,
		"isFinished":                     false,
		"isPrivate":                      false,
		"isStalled":                      false,
		"labels":                         []string{},
		"leftUntilDone":                  0,
		"magnetLink":                     "",
		"manualAnnounceTime":             0,
		"maxConnectedPeers":              0,
		"metadataPercentComplete":        1,
		"name":                           "",
		"peer-limit":                     0,
		"peers":                          []JsonMap{},
		"peersConnected":                 0,
		"peersFrom":                      PeersFromTemplate(),
		"peersGettingFromUs":             0,
		"peersSendingToUs":               0,
		"percentComplete":                0,
		"percentDone":                    0,
		"pieces":                         "",
		"pieceCount":                     0,
		"pieceSize":                      0,
		"priorities":                     []int{},
		"primary-mime-type":              "application/octet-stream", // Transmission's fallback
		"queuePosition":                  0,
		"rateDownload":                   0,
		"rateUpload":                     0,
		"recheckProgress":                0,
		"secondsDownloading":             0,
		"secondsSeeding":                 0,
		"seedIdleLimit":                  0,
		"seedIdleMode":                   0, // TR_IDLELIMIT_GLOBAL
		"seedRatioLimit":                 0,
		"seedRatioMode":                  0, // TR_RATIOLIMIT_GLOBAL
		"sequential_download":            false,
		"sequential_download_from_piece": 0,
		"sizeWhenDone":                   0,
		"startDate":                      0,
		"status":                         0,
		"torrentFile":                    "",
		"totalSize":                      0,
		"trackers":                       []JsonMap{},
		"trackerList":                    "",
		"trackerStats":                   []JsonMap{},
		"uploadedEver":                   0,
		"uploadLimit":                    0,
		"uploadLimited":                  false,
		"uploadRatio":                    0,
		"wanted":                         []int{},
		"webseeds":                       []string{},
		"webseedsSendingToUs":            0,
	}
}

// TrackerStatsTemplate: neutral trackerStats values (rpc-spec.md 4.1.4 §3.3
// `tracker_stats`, old camelCase names). -1 is Transmission's "unknown" for counts.
func TrackerStatsTemplate() JsonMap {
	return JsonMap{
		"announce":              "",
		"announceState":         0, // TR_TRACKER_INACTIVE
		"downloadCount":         -1,
		"hasAnnounced":          false,
		"hasScraped":            false,
		"host":                  "",
		"id":                    0,
		"isBackup":              false,
		"lastAnnouncePeerCount": 0,
		"lastAnnounceResult":    "",
		"lastAnnounceStartTime": 0,
		"lastAnnounceSucceeded": false,
		"lastAnnounceTime":      0,
		"lastAnnounceTimedOut":  false,
		"lastScrapeResult":      "",
		"lastScrapeStartTime":   0,
		"lastScrapeSucceeded":   false,
		"lastScrapeTime":        0,
		// rpc-spec 4.1.4 says boolean, but hekmon/transmissionrpc v1 (and old
		// specs) decode a number; 0 is falsy for JS clients like TrguiNG.
		"lastScrapeTimedOut": 0,
		"leecherCount":       -1,
		"nextAnnounceTime":   0,
		"nextScrapeTime":     0,
		"scrape":             "",
		"scrapeState":        0,
		"seederCount":        -1,
		"sitename":           "",
		"tier":               0,
	}
}
