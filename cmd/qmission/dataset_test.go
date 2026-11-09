package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Willian-Zhang/qmission/qBT"
	"gopkg.in/h2non/gock.v1"
)

// datasetDirs are the per-version recordings in testdata/ (see testdata/README.md).
// Adding a version is adding its directory name here; missing dirs are skipped.
var datasetDirs = []string{"4.6", "5.3"}

// TrguiNG's field lists, copied from TrguiNG src/rpc/transmission.ts.
var (
	trguingTorrentMinimumFields = []string{
		"id", "name", "status", "pieceCount", "downloadDir", "labels", "error", "errorString",
		"trackerStats", "magnetLink", "rateDownload", "rateUpload", "sizeWhenDone", "haveValid",
	}
	// TorrentFields: TorrentMinimumFields plus every column; the list poll with all columns visible.
	trguingTorrentFields = append(append([]string{}, trguingTorrentMinimumFields...),
		"activityDate", "addedDate", "bandwidthPriority", "corruptEver", "dateCreated", "desiredAvailable",
		"doneDate", "downloadedEver", "downloadLimit", "downloadLimited", "editDate", "eta", "etaIdle",
		"file-count", "group", "hashString", "haveUnchecked", "honorsSessionLimits", "isFinished",
		"isPrivate", "isStalled", "leftUntilDone", "manualAnnounceTime", "maxConnectedPeers",
		"metadataPercentComplete", "peer-limit", "peersConnected", "peersGettingFromUs",
		"peersSendingToUs", "percentComplete", "percentDone", "pieceSize", "queuePosition",
		"recheckProgress", "secondsDownloading", "secondsSeeding", "seedIdleLimit", "seedIdleMode",
		"seedRatioLimit", "seedRatioMode", "sequential_download", "startDate", "totalSize",
		"torrentFile", "uploadedEver", "uploadLimit", "uploadLimited", "uploadRatio", "webseedsSendingToUs",
	)
	trguingSessionFields = []string{
		"alt-speed-down", "alt-speed-enabled", "alt-speed-up", "download-dir-free-space",
		"speed-limit-down-enabled", "speed-limit-down", "speed-limit-up-enabled", "speed-limit-up",
	}
	trguingSessionAllFields = append(append([]string{}, trguingSessionFields...),
		"alt-speed-time-begin", "alt-speed-time-day", "alt-speed-time-enabled", "alt-speed-time-end",
		"blocklist-enabled", "blocklist-size", "blocklist-url", "cache-size-mb", "config-dir",
		"default-trackers", "dht-enabled", "download-dir", "download-queue-enabled", "download-queue-size",
		"encryption", "idle-seeding-limit-enabled", "idle-seeding-limit", "incomplete-dir-enabled",
		"incomplete-dir", "lpd-enabled", "peer-limit-global", "peer-limit-per-torrent",
		"peer-port-random-on-start", "peer-port", "pex-enabled", "port-forwarding-enabled",
		"queue-stalled-enabled", "queue-stalled-minutes", "rename-partial-files", "rpc-version-minimum",
		"rpc-version-semver", "rpc-version", "script-torrent-added-enabled", "script-torrent-added-filename",
		"script-torrent-done-enabled", "script-torrent-done-filename", "script-torrent-done-seeding-enabled",
		"script-torrent-done-seeding-filename", "seed-queue-enabled", "seed-queue-size", "seedRatioLimit",
		"seedRatioLimited", "sequential_download", "start-added-torrents", "trash-original-torrent-files",
		"units", "utp-enabled", "version",
	)
)

// Expected Transmission status per qBT state, written independently of the code
// under test (rpc-spec.md 4.1.4 `status`; "moving" depends on progress).
var expectedStatus = map[string]int{
	"error": 0, "missingFiles": 0, "unknown": 0,
	"stoppedUP": 0, "pausedUP": 0, "stoppedDL": 0, "pausedDL": 0,
	"checkingResumeData": 1, "checkingUP": 2, "checkingDL": 2,
	"queuedDL": 3, "downloading": 4, "stalledDL": 4, "forcedDL": 4, "metaDL": 4, "forcedMetaDL": 4,
	"queuedUP": 5, "uploading": 6, "stalledUP": 6, "forcedUP": 6,
}

// datasetSpotChecks are hand-checked values for one recording, keyed by hash.
var datasetSpotChecks = map[string]map[string]map[string]interface{}{
	"4.6": {
		"0000000000000000000000000000000000000001": {"id": 1.0, "status": 6.0, "eta": -1.0, "pieceCount": 987.0,
			"doneDate": 1794244959.0, "queuePosition": 3.0, "labels": []interface{}{"tag1"}},
		"0000000000000000000000000000000000000002": {"id": 2.0, "status": 4.0, "eta": 2410.0, "pieceCount": 3024.0,
			"doneDate": 0.0, "queuePosition": 1.0, "labels": []interface{}{"tag2"}, "downloadLimit": 300.0, "downloadLimited": true},
		"0000000000000000000000000000000000000003": {"id": 3.0, "status": 4.0, "pieceCount": 0.0, "metadataPercentComplete": 0.0,
			"totalSize": 0.0, "queuePosition": 2.0, "labels": []interface{}{}},
		"0000000000000000000000000000000000000004": {"id": 4.0, "status": 0.0, "pieceCount": 3056.0, "queuePosition": 0.0},
	},
}

type dataset struct {
	name, dir   string
	webapi, app string
	prefs       map[string]interface{}
	syncFiles   []map[string]interface{} // sync_initial, sync_1, ...
	props       map[string]map[string]interface{}
}

func readJSONFile(t *testing.T, path string, dst interface{}) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func readText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

// forEachDataset runs fn once per recorded qBT version, with gock serving that recording.
func forEachDataset(t *testing.T, fn func(t *testing.T, ds *dataset, c *rpcClient)) {
	for _, name := range datasetDirs {
		name := name
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join("testdata", name)
			if _, err := os.Stat(dir); err != nil {
				t.Skipf("no recording in %s", dir)
			}
			quietLogs(t)
			t.Cleanup(gock.Off)
			ds := loadDataset(t, name, dir)
			ds.mock(t)
			initTestQBT(t, true)
			_, c := newTestServer(t)
			fn(t, ds, c)
		})
	}
}

func loadDataset(t *testing.T, name, dir string) *dataset {
	ds := &dataset{name: name, dir: dir, props: map[string]map[string]interface{}{}}
	ds.webapi = readText(t, filepath.Join(dir, "webapi_version.txt"))
	ds.app = readText(t, filepath.Join(dir, "app_version.txt"))
	readJSONFile(t, filepath.Join(dir, "preferences.json"), &ds.prefs)
	var initial map[string]interface{}
	readJSONFile(t, filepath.Join(dir, "sync_initial.json"), &initial)
	ds.syncFiles = append(ds.syncFiles, initial)
	for n := 1; ; n++ {
		path := filepath.Join(dir, fmt.Sprintf("sync_%d.json", n))
		if _, err := os.Stat(path); err != nil {
			break
		}
		var delta map[string]interface{}
		readJSONFile(t, path, &delta)
		ds.syncFiles = append(ds.syncFiles, delta)
	}
	propsFiles, _ := filepath.Glob(filepath.Join(dir, "torrent_*_properties.json"))
	for _, path := range propsFiles {
		var p map[string]interface{}
		readJSONFile(t, path, &p)
		hash, _ := p["hash"].(string)
		if hash == "" {
			t.Fatalf("%s: no hash", path)
		}
		ds.props[hash] = p
	}
	return ds
}

func (ds *dataset) mock(t *testing.T) {
	mockTestLogin(ds.webapi, ds.app)
	gock.New(testQBTAddr).Get("/api/v2/app/preferences").Persist().Reply(200).File(filepath.Join(ds.dir, "preferences.json"))
	gock.New(testQBTAddr).Get("/api/v2/torrents/info").Persist().Reply(200).File(filepath.Join(ds.dir, "torrent_list.json"))

	// sync_initial answers rid=0, sync_N answers rid=N; after the last file the
	// table stays as it is.
	gock.New(testQBTAddr).Get("/api/v2/sync/maindata").MatchParam("rid", "^0$").
		Reply(200).File(filepath.Join(ds.dir, "sync_initial.json"))
	for n := 1; n < len(ds.syncFiles); n++ {
		gock.New(testQBTAddr).Get("/api/v2/sync/maindata").MatchParam("rid", fmt.Sprintf("^%d$", n)).
			Reply(200).File(filepath.Join(ds.dir, fmt.Sprintf("sync_%d.json", n)))
	}
	lastRid := int(ds.syncFiles[len(ds.syncFiles)-1]["rid"].(float64))
	gock.New(testQBTAddr).Get("/api/v2/sync/maindata").MatchParam("rid", fmt.Sprintf("^%d$", lastRid)).
		Persist().Reply(200).BodyString(fmt.Sprintf(`{"rid": %d}`, lastRid))

	re := regexp.MustCompile(`torrent_(\d+)_properties\.json$`)
	propsFiles, _ := filepath.Glob(filepath.Join(ds.dir, "torrent_*_properties.json"))
	for _, path := range propsFiles {
		n := re.FindStringSubmatch(path)[1]
		var p map[string]interface{}
		readJSONFile(t, path, &p)
		hash := "^" + p["hash"].(string) + "$"
		file := func(kind string) string { return filepath.Join(ds.dir, "torrent_"+n+"_"+kind+".json") }
		gock.New(testQBTAddr).Get("/api/v2/torrents/properties").MatchParam("hash", hash).Persist().Reply(200).File(path)
		gock.New(testQBTAddr).Get("/api/v2/torrents/trackers").MatchParam("hash", hash).Persist().Reply(200).File(file("trackers"))
		gock.New(testQBTAddr).Get("/api/v2/torrents/pieceStates").MatchParam("hash", hash).Persist().Reply(200).File(file("piecestates"))
		gock.New(testQBTAddr).Get("/api/v2/torrents/files").MatchParam("hash", hash).Persist().Reply(200).File(file("files"))
		gock.New(testQBTAddr).Get("/api/v2/sync/torrentPeers").MatchParam("hash", hash).Persist().Reply(200).File(file("peers"))
	}
}

// torrentsAfter merges sync files [0..n] into a hash -> fields table, like qBT's WebUI does.
func (ds *dataset) torrentsAfter(n int) map[string]map[string]interface{} {
	table := map[string]map[string]interface{}{}
	for _, f := range ds.syncFiles[:n+1] {
		if full, _ := f["full_update"].(bool); full {
			table = map[string]map[string]interface{}{}
		}
		torrents, _ := f["torrents"].(map[string]interface{})
		for hash, raw := range torrents {
			if table[hash] == nil {
				table[hash] = map[string]interface{}{}
			}
			for k, v := range raw.(map[string]interface{}) {
				table[hash][k] = v
			}
		}
		removed, _ := f["torrents_removed"].([]interface{})
		for _, h := range removed {
			delete(table, h.(string))
		}
	}
	return table
}

func (ds *dataset) serverStateAfter(n int) map[string]interface{} {
	state := map[string]interface{}{}
	for _, f := range ds.syncFiles[:n+1] {
		s, _ := f["server_state"].(map[string]interface{})
		for k, v := range s {
			state[k] = v
		}
	}
	return state
}

func num(m map[string]interface{}, key string) float64 {
	v, _ := m[key].(float64)
	return v
}

func kib(b float64) float64 { return math.Max(1, math.Floor((b+512)/1024)) }

func TestDatasetSessionGet(t *testing.T) {
	forEachDataset(t, func(t *testing.T, ds *dataset, c *rpcClient) {
		s := c.mustSucceed("session-get", nil)
		for _, f := range trguingSessionAllFields {
			if _, ok := s[f]; !ok {
				t.Errorf("session-get: %s missing", f)
			}
		}
		want := map[string]interface{}{
			"rpc-version":             18.0,
			"rpc-version-minimum":     14.0,
			"rpc-version-semver":      "5.3.0",
			"version":                 "4.1.0 (qmission, qBT " + strings.TrimPrefix(ds.app, "v") + ")",
			"download-dir-free-space": num(ds.serverStateAfter(0), "free_space_on_disk"),
			"session-id":              sessionID,
		}
		addStopped, _ := ds.prefs["add_stopped_enabled"].(bool)
		startPaused, _ := ds.prefs["start_paused_enabled"].(bool)
		want["start-added-torrents"] = !(addStopped || startPaused)
		for key, pref := range map[string]string{"speed-limit-down": "dl_limit", "speed-limit-up": "up_limit",
			"alt-speed-down": "alt_dl_limit", "alt-speed-up": "alt_up_limit"} {
			if b := num(ds.prefs, pref); b > 0 {
				want[key] = kib(b)
			} else {
				want[key] = 0.0
			}
		}
		want["speed-limit-down-enabled"] = num(ds.prefs, "dl_limit") > 0
		want["speed-limit-up-enabled"] = num(ds.prefs, "up_limit") > 0
		for k, v := range want {
			if !reflect.DeepEqual(s[k], v) {
				t.Errorf("session-get %s = %#v, want %#v", k, s[k], v)
			}
		}
		units := s["units"].(map[string]interface{})
		if units["speed-bytes"] != 1024.0 || !reflect.DeepEqual(units["speed-units"], []interface{}{"KiB/s", "MiB/s", "GiB/s", "TiB/s"}) {
			t.Errorf("units = %v", units)
		}

		// TrguiNG's per-poll request: exactly the asked fields come back.
		s = c.mustSucceed("session-get", map[string]interface{}{"fields": trguingSessionFields})
		if len(s) != len(trguingSessionFields) {
			t.Errorf("session-get with fields: %d keys, want %d", len(s), len(trguingSessionFields))
		}
	})
}

func TestDatasetSessionStats(t *testing.T) {
	forEachDataset(t, func(t *testing.T, ds *dataset, c *rpcClient) {
		s := c.mustSucceed("session-stats", nil)
		torrents := ds.torrentsAfter(0)
		state := ds.serverStateAfter(0)
		active := 0
		for _, tor := range torrents {
			if expectedStatus[tor["state"].(string)] != 0 {
				active++
			}
		}
		want := map[string]float64{
			"torrentCount":       float64(len(torrents)),
			"activeTorrentCount": float64(active),
			"pausedTorrentCount": float64(len(torrents) - active),
			"downloadSpeed":      num(state, "dl_info_speed"),
			"uploadSpeed":        num(state, "up_info_speed"),
		}
		for k, v := range want {
			if s[k] != v {
				t.Errorf("session-stats %s = %v, want %v", k, s[k], v)
			}
		}
		cur := s["current-stats"].(map[string]interface{})
		cum := s["cumulative-stats"].(map[string]interface{})
		if cur["downloadedBytes"] != num(state, "dl_info_data") || cur["uploadedBytes"] != num(state, "up_info_data") {
			t.Errorf("current-stats = %v", cur)
		}
		if cum["downloadedBytes"] != num(state, "alltime_dl") || cum["uploadedBytes"] != num(state, "alltime_ul") {
			t.Errorf("cumulative-stats = %v", cum)
		}
	})
}

func expectedTrackerHost(announce string) string {
	u, err := url.Parse(announce)
	if err != nil {
		return announce
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"udp": "80", "http": "80", "https": "443", "ws": "80", "wss": "443"}[u.Scheme]
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// checkListTorrent compares one torrent of a TrguiNG list poll with the raw qBT data.
func checkListTorrent(t *testing.T, ds *dataset, fields []string, got, raw map[string]interface{}) {
	t.Helper()
	hash := got["hashString"].(string)
	state := raw["state"].(string)
	for _, f := range fields {
		if _, ok := got[f]; !ok {
			t.Errorf("%s: field %s missing", hash, f)
		}
	}
	if len(got) != len(fields) {
		t.Errorf("%s: %d fields, want %d", hash, len(got), len(fields))
	}

	status, ok := expectedStatus[state]
	if !ok && state == "moving" {
		status = 4
		if num(raw, "progress") >= 1 {
			status = 6
		}
	}
	metaDL := state == "metaDL" || state == "forcedMetaDL"
	wantPieces := 0.0
	if p, ok := ds.props[hash]; ok && !metaDL {
		wantPieces = num(p, "pieces_num")
	}
	eta := num(raw, "eta")
	switch {
	case eta >= 8640000 && status == 4:
		eta = -2
	case eta >= 8640000 || eta < 0:
		eta = -1
	}
	done := num(raw, "completion_on")
	if done <= 0 || done >= 4294967295 {
		done = 0
	}
	labels := []interface{}{}
	for _, tag := range strings.Split(fmt.Sprint(raw["tags"]), ",") {
		if tag = strings.TrimSpace(tag); tag != "" && tag != "<nil>" {
			labels = append(labels, tag)
		}
	}
	errCode := 0.0
	if state == "error" || state == "missingFiles" {
		errCode = 3
	}
	metaPct := 1.0
	if metaDL {
		metaPct = 0
	}
	want := map[string]interface{}{
		"status":                  float64(status),
		"error":                   errCode,
		"pieceCount":              wantPieces,
		"haveValid":               math.Max(num(raw, "completed"), 0),
		"leftUntilDone":           math.Max(num(raw, "amount_left"), 0),
		"sizeWhenDone":            math.Max(num(raw, "size"), 0),
		"totalSize":               math.Max(num(raw, "total_size"), 0),
		"rateDownload":            num(raw, "dlspeed"),
		"rateUpload":              num(raw, "upspeed"),
		"eta":                     eta,
		"doneDate":                done,
		"labels":                  labels,
		"sequential_download":     raw["seq_dl"],
		"magnetLink":              raw["magnet_uri"],
		"isStalled":               state == "stalledDL" || state == "stalledUP",
		"metadataPercentComplete": metaPct,
		"downloadedEver":          math.Max(num(raw, "downloaded"), 0),
		"uploadedEver":            math.Max(num(raw, "uploaded"), 0),
	}
	for k, v := range want {
		if !Any(fields, k) {
			continue
		}
		if !reflect.DeepEqual(got[k], v) {
			t.Errorf("%s (%s): %s = %#v, want %#v", hash, state, k, got[k], v)
		}
	}

	stats := got["trackerStats"].([]interface{})
	tracker, _ := raw["tracker"].(string)
	switch {
	case tracker != "":
		if len(stats) != 1 {
			t.Fatalf("%s: %d trackerStats, want 1", hash, len(stats))
		}
		st := stats[0].(map[string]interface{})
		if st["announce"] != tracker || st["host"] != expectedTrackerHost(tracker) ||
			st["seederCount"] != num(raw, "num_complete") || st["leecherCount"] != num(raw, "num_incomplete") ||
			st["hasAnnounced"] != false {
			t.Errorf("%s: trackerStats = %v", hash, st)
		}
	case num(raw, "trackers_count") == 0:
		if len(stats) != 0 {
			t.Errorf("%s: trackerStats = %v, want []", hash, stats)
		}
	default:
		if len(stats) != 1 || stats[0].(map[string]interface{})["host"] != "" {
			t.Errorf("%s: trackerStats = %v, want one entry without host", hash, stats)
		}
	}
}

func TestDatasetTorrentListPoll(t *testing.T) {
	forEachDataset(t, func(t *testing.T, ds *dataset, c *rpcClient) {
		for poll := 0; poll < len(ds.syncFiles); poll++ {
			fields := trguingTorrentFields
			if poll%2 == 1 {
				fields = append(append([]string{}, trguingTorrentMinimumFields...), "hashString")
			}
			out := c.mustSucceed("torrent-get", map[string]interface{}{"fields": fields})
			raw := ds.torrentsAfter(poll)
			list := out["torrents"].([]interface{})
			if len(list) != len(raw) {
				t.Fatalf("poll %d: %d torrents, want %d", poll, len(list), len(raw))
			}
			ids := map[float64]bool{}
			var positions []float64
			for _, item := range list {
				got := item.(map[string]interface{})
				hash := got["hashString"].(string)
				r, ok := raw[hash]
				if !ok {
					t.Fatalf("poll %d: unexpected torrent %s", poll, hash)
				}
				id := got["id"].(float64)
				if id < 1 || ids[id] {
					t.Errorf("poll %d: bad or duplicate id %v", poll, id)
				}
				ids[id] = true
				checkListTorrent(t, ds, fields, got, r)
				if pos, ok := got["queuePosition"].(float64); ok {
					positions = append(positions, pos)
				}
				if poll == 0 {
					for k, v := range datasetSpotChecks[ds.name][hash] {
						if !reflect.DeepEqual(got[k], v) {
							t.Errorf("spot check %s: %s = %#v, want %#v", hash, k, got[k], v)
						}
					}
				}
			}
			if len(positions) > 0 {
				sort.Float64s(positions)
				for i, p := range positions {
					if p != float64(i) {
						t.Errorf("poll %d: queue positions %v are not 0..n-1", poll, positions)
						break
					}
				}
			}
		}
	})
}

// TestDatasetDetails checks the single-ID path (real trackers, props) runs on
// every recording without errors; values are covered in stage 1b.
func TestDatasetDetails(t *testing.T) {
	forEachDataset(t, func(t *testing.T, ds *dataset, c *rpcClient) {
		list := c.mustSucceed("torrent-get", map[string]interface{}{"fields": []string{"id", "hashString"}})["torrents"].([]interface{})
		for _, item := range list {
			tor := item.(map[string]interface{})
			if _, ok := ds.props[tor["hashString"].(string)]; !ok {
				continue
			}
			out := c.mustSucceed("torrent-get", map[string]interface{}{
				"ids":    []interface{}{tor["id"]},
				"fields": append(append([]string{}, trguingTorrentFields...), "trackers", "trackerList", "comment", "creator", "peersFrom"),
			})
			got := out["torrents"].([]interface{})[0].(map[string]interface{})
			for _, st := range got["trackerStats"].([]interface{}) {
				if a := st.(map[string]interface{})["announce"].(string); strings.HasPrefix(a, "** [") {
					t.Errorf("pseudo tracker %q in trackerStats", a)
				}
			}
			if got["pieceCount"] != num(ds.props[tor["hashString"].(string)], "pieces_num") {
				t.Errorf("details pieceCount = %v", got["pieceCount"])
			}
		}
	})
}

// TestDatasetStopStartGates: torrent-stop/start use pause/resume below WebAPI 2.11 and stop/start from it.
func TestDatasetStopStartGates(t *testing.T) {
	forEachDataset(t, func(t *testing.T, ds *dataset, c *rpcClient) {
		v, err := qBT.ParseAPIVersion(ds.webapi)
		if err != nil {
			t.Fatal(err)
		}
		gates := qBT.GatesFor(v)
		wantStop, wantStart := "/api/v2/torrents/pause", "/api/v2/torrents/resume"
		if v.AtLeast(qBT.StopStartAPIVersion) {
			wantStop, wantStart = "/api/v2/torrents/stop", "/api/v2/torrents/start"
		}
		if "/api/v2/"+gates.StopEndpoint != wantStop {
			t.Fatalf("gate %s, want %s", gates.StopEndpoint, wantStop)
		}
		gock.New(testQBTAddr).Post(wantStop).Reply(200)
		gock.New(testQBTAddr).Post(wantStart).Reply(200)
		c.mustSucceed("torrent-stop", map[string]interface{}{"ids": []int{1}})
		c.mustSucceed("torrent-start", map[string]interface{}{"ids": []int{1}})
		out := c.call("torrent-stop", map[string]interface{}{"ids": []int{1}})
		if out["result"] == "success" {
			t.Error("third stop should fail: no mock left")
		}
	})
}
