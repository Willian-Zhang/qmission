package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"math/bits"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/Willian-Zhang/qmission/qBT"
	"gopkg.in/h2non/gock.v1"
)

// trguingTorrentAllFields: TrguiNG's details request (src/rpc/transmission.ts
// TorrentAllFields = TorrentFields + these), sent with ids: [id].
var trguingTorrentAllFields = append(append([]string{}, trguingTorrentFields...),
	"comment", "creator", "files", "fileStats", "peers", "peersFrom", "pieces", "priorities",
	"primary-mime-type", "trackers", "trackerList", "wanted", "webseeds")

var peersFromKeys = []string{"fromCache", "fromDht", "fromIncoming", "fromLpd", "fromLtep", "fromPex", "fromTracker"}

// Expected Transmission priority per qBT file priority, written independently
// of the code (plan.md Decisions, Xmister read mapping).
var expectedFilePriority = map[float64]float64{0: 0, 1: -1, 2: 0, 3: 0, 4: 0, 5: 0, 6: 0, 7: 1}

// countRequests counts qBT API calls by path until the test ends.
func countRequests(t *testing.T) (reset func(), counts func() map[string]int) {
	var mu sync.Mutex
	seen := map[string]int{}
	gock.Observe(func(req *http.Request, _ gock.Mock) {
		mu.Lock()
		seen[strings.TrimPrefix(req.URL.Path, "/api/v2/")]++
		mu.Unlock()
	})
	t.Cleanup(func() { gock.Observe(nil) })
	reset = func() {
		mu.Lock()
		seen = map[string]int{}
		mu.Unlock()
	}
	counts = func() map[string]int {
		mu.Lock()
		defer mu.Unlock()
		out := make(map[string]int, len(seen))
		for k, v := range seen {
			out[k] = v
		}
		return out
	}
	return
}

// recordings maps hash -> torrent_<n> file prefix for the torrents that have
// per-torrent recordings in the dataset.
func recordings(t *testing.T, ds *dataset) map[string]string {
	re := regexp.MustCompile(`torrent_(\d+)_properties\.json$`)
	files, _ := filepath.Glob(filepath.Join(ds.dir, "torrent_*_properties.json"))
	out := map[string]string{}
	for _, f := range files {
		var p map[string]interface{}
		readJSONFile(t, f, &p)
		out[p["hash"].(string)] = filepath.Join(ds.dir, "torrent_"+re.FindStringSubmatch(f)[1]+"_")
	}
	return out
}

func readJSONIfExists(t *testing.T, file string, dst interface{}) bool {
	if _, err := os.Stat(file); err != nil {
		return false
	}
	readJSONFile(t, file, dst)
	return true
}

const fakeWebseed = "https://webseed.example/files/"

// Fake answers for a metaDL torrent without per-torrent recordings.
const (
	metaDLTrackers = `[{"url":"** [DHT] **","tier":-1,"status":2,"num_peers":0,"num_seeds":0,"num_leeches":0,"num_downloaded":0,"msg":""},
		{"url":"** [PeX] **","tier":-1,"status":2,"num_peers":0,"num_seeds":0,"num_leeches":0,"num_downloaded":0,"msg":""},
		{"url":"** [LSD] **","tier":-1,"status":2,"num_peers":0,"num_seeds":0,"num_leeches":0,"num_downloaded":0,"msg":""},
		{"url":"udp://tracker.example:6969/announce","tier":0,"status":1,"num_peers":-1,"num_seeds":-1,"num_leeches":-1,"num_downloaded":-1,"msg":""}]`
	metaDLPeers = `{"full_update":true,"peers":{},"rid":1,"show_flags":true}`
)

// TestDatasetDetailsPane: TrguiNG's details request for every recorded torrent
// (and metaDL torrents), checked against the raw qBT recordings, with at most
// one call per endpoint.
func TestDatasetDetailsPane(t *testing.T) {
	forEachDataset(t, func(t *testing.T, ds *dataset, c *rpcClient) {
		gock.New(testQBTAddr).Get("/api/v2/torrents/webseeds").Persist().Reply(200).
			JSON([]map[string]string{{"url": fakeWebseed}})
		rec := recordings(t, ds)
		reset, counts := countRequests(t)

		// Run through every recorded sync so the table is final.
		var list []interface{}
		for i := 0; i < len(ds.syncFiles); i++ {
			list = c.mustSucceed("torrent-get", map[string]interface{}{"fields": []string{"id", "hashString"}})["torrents"].([]interface{})
		}
		raw := ds.torrentsAfter(len(ds.syncFiles) - 1)

		checked, metaChecked := 0, 0
		for _, item := range list {
			tor := item.(map[string]interface{})
			hash := tor["hashString"].(string)
			state, _ := raw[hash]["state"].(string)
			metaDL := state == "metaDL" || state == "forcedMetaDL"
			prefix, recorded := rec[hash]
			switch {
			case recorded:
			case metaDL:
				gock.New(testQBTAddr).Get("/api/v2/torrents/trackers").MatchParam("hash", "^"+hash+"$").Persist().Reply(200).BodyString(metaDLTrackers)
				gock.New(testQBTAddr).Get("/api/v2/sync/torrentPeers").MatchParam("hash", "^"+hash+"$").Persist().Reply(200).BodyString(metaDLPeers)
			default:
				continue
			}

			reset()
			out := c.mustSucceed("torrent-get", map[string]interface{}{"ids": []interface{}{tor["id"]}, "fields": trguingTorrentAllFields})
			torrents := out["torrents"].([]interface{})
			if len(torrents) != 1 {
				t.Fatalf("%s: %d torrents", hash, len(torrents))
			}
			got := torrents[0].(map[string]interface{})
			for _, f := range trguingTorrentAllFields {
				if _, ok := got[f]; !ok {
					t.Errorf("%s: field %s missing", hash, f)
				}
			}

			calls := counts()
			for p, n := range calls {
				if n > 1 {
					t.Errorf("%s: %d calls to %s, want at most 1", hash, n, p)
				}
			}
			for _, p := range []string{"torrents/trackers", "sync/torrentPeers", "torrents/webseeds"} {
				if calls[p] != 1 {
					t.Errorf("%s: %d calls to %s, want 1", hash, calls[p], p)
				}
			}

			checkDetailsPeers(t, hash, prefix, recorded, got)
			if got["webseeds"] == nil || !reflect.DeepEqual(got["webseeds"], []interface{}{fakeWebseed}) {
				t.Errorf("%s: webseeds = %v", hash, got["webseeds"])
			}

			if metaDL {
				metaChecked++
				for _, p := range []string{"torrents/files", "torrents/pieceStates", "torrents/properties"} {
					if calls[p] != 0 {
						t.Errorf("metaDL %s: %d calls to %s, want none", hash, calls[p], p)
					}
				}
				for _, f := range []string{"files", "fileStats", "priorities", "wanted"} {
					if l, ok := got[f].([]interface{}); !ok || len(l) != 0 {
						t.Errorf("metaDL %s: %s = %v, want []", hash, f, got[f])
					}
				}
				if got["pieces"] != "" || got["pieceCount"] != 0.0 || got["file-count"] != 0.0 ||
					got["primary-mime-type"] != "application/octet-stream" {
					t.Errorf("metaDL %s: pieces %q pieceCount %v file-count %v mime %v", hash, got["pieces"],
						got["pieceCount"], got["file-count"], got["primary-mime-type"])
				}
				var rawTrackers []map[string]interface{}
				if !readJSONIfExists(t, prefix+"trackers.json", &rawTrackers) {
					if err := jsonUnmarshalString(metaDLTrackers, &rawTrackers); err != nil {
						t.Fatal(err)
					}
				}
				checkDetailsTrackers(t, hash, rawTrackers, got)
				continue
			}

			checked++
			for _, p := range []string{"torrents/files", "torrents/pieceStates", "torrents/properties"} {
				if calls[p] != 1 {
					t.Errorf("%s: %d calls to %s, want 1", hash, calls[p], p)
				}
			}
			props := ds.props[hash]
			var rawFiles, rawTrackers []map[string]interface{}
			var rawPieces []float64
			readJSONFile(t, prefix+"files.json", &rawFiles)
			readJSONFile(t, prefix+"trackers.json", &rawTrackers)
			readJSONFile(t, prefix+"piecestates.json", &rawPieces)
			checkDetailsFiles(t, hash, rawFiles, got)
			checkDetailsTrackers(t, hash, rawTrackers, got)
			checkDetailsPieces(t, hash, rawPieces, got)

			want := map[string]interface{}{
				"pieceCount":        num(props, "pieces_num"),
				"pieceSize":         num(props, "piece_size"),
				"comment":           props["comment"],
				"creator":           props["created_by"],
				"corruptEver":       num(props, "total_wasted"),
				"maxConnectedPeers": num(props, "nb_connections_limit"),
				"peer-limit":        num(props, "nb_connections_limit"),
				"isPrivate":         props["is_private"],
				"dateCreated":       num(props, "creation_date"),
			}
			if l, ok := raw[hash]; ok {
				limit := num(l, "dl_limit")
				want["downloadLimited"] = limit > 0
				want["downloadLimit"] = 0.0
				if limit > 0 {
					want["downloadLimit"] = kib(limit)
				}
				switch r := num(l, "ratio_limit"); {
				case r == -2:
					want["seedRatioMode"] = 0.0
				case r < 0:
					want["seedRatioMode"] = 2.0
				default:
					want["seedRatioMode"], want["seedRatioLimit"] = 1.0, r
				}
				if idle, ok := l["inactive_seeding_time_limit"].(float64); ok {
					switch {
					case idle == -2:
						want["seedIdleMode"] = 0.0
					case idle < 0:
						want["seedIdleMode"] = 2.0
					default:
						want["seedIdleMode"], want["seedIdleLimit"] = 1.0, idle
					}
				}
			}
			for k, v := range want {
				if !reflect.DeepEqual(got[k], v) {
					t.Errorf("%s: %s = %#v, want %#v", hash, k, got[k], v)
				}
			}
		}
		if checked == 0 {
			t.Error("no recorded torrent checked")
		}
		t.Logf("%s: %d recorded torrents, %d metaDL torrents checked", ds.name, checked, metaChecked)
	})
}

func checkDetailsFiles(t *testing.T, hash string, rawFiles []map[string]interface{}, got map[string]interface{}) {
	t.Helper()
	files, _ := got["files"].([]interface{})
	stats, _ := got["fileStats"].([]interface{})
	prios, _ := got["priorities"].([]interface{})
	wanted, _ := got["wanted"].([]interface{})
	n := len(rawFiles)
	if len(files) != n || len(stats) != n || len(prios) != n || len(wanted) != n || got["file-count"] != float64(n) {
		t.Fatalf("%s: %d files, %d fileStats, %d priorities, %d wanted, file-count %v; want %d",
			hash, len(files), len(stats), len(prios), len(wanted), got["file-count"], n)
	}
	sizeByExt := map[string]float64{}
	for i, rf := range rawFiles {
		f := files[i].(map[string]interface{})
		st := stats[i].(map[string]interface{})
		size, progress := num(rf, "size"), num(rf, "progress")
		done := math.Round(size * progress)
		switch {
		case progress >= 1:
			done = size
		case done >= size:
			done = size - 1
		}
		prio := num(rf, "priority")
		if f["name"] != rf["name"] || f["length"] != size || f["bytesCompleted"] != done || st["bytesCompleted"] != done {
			t.Errorf("%s file %d: %v, raw size %v progress %v", hash, i, f, size, progress)
		}
		if pr, ok := rf["piece_range"].([]interface{}); ok && len(pr) == 2 {
			if f["begin_piece"] != pr[0] || f["end_piece"] != pr[1].(float64)+1 {
				t.Errorf("%s file %d: pieces %v-%v, raw piece_range %v", hash, i, f["begin_piece"], f["end_piece"], pr)
			}
		}
		wantWanted := 0.0
		if prio != 0 {
			wantWanted = 1
		}
		if st["wanted"] != (prio != 0) || wanted[i] != wantWanted {
			t.Errorf("%s file %d: wanted %v/%v for qBT priority %v", hash, i, st["wanted"], wanted[i], prio)
		}
		if st["priority"] != expectedFilePriority[prio] || prios[i] != expectedFilePriority[prio] {
			t.Errorf("%s file %d: priority %v/%v for qBT priority %v", hash, i, st["priority"], prios[i], prio)
		}
		sizeByExt[strings.ToLower(path.Ext(rf["name"].(string)))] += size
	}
	mimeType, _ := got["primary-mime-type"].(string)
	if !strings.Contains(mimeType, "/") {
		t.Errorf("%s: primary-mime-type %q", hash, mimeType)
	}
	// Largest extension by bytes; checked when it's one with a fixed type.
	bestExt, best := "", -1.0
	for ext, size := range sizeByExt {
		if size > best || (size == best && ext < bestExt) {
			bestExt, best = ext, size
		}
	}
	if want, ok := map[string]string{".iso": "application/x-iso9660-image", ".mp4": "video/mp4",
		".mkv": "video/x-matroska", ".srt": "application/x-subrip"}[bestExt]; ok && mimeType != want {
		t.Errorf("%s: primary-mime-type %q, want %q (largest extension %s)", hash, mimeType, want, bestExt)
	}
}

func checkDetailsTrackers(t *testing.T, hash string, rawTrackers []map[string]interface{}, got map[string]interface{}) {
	t.Helper()
	var real []map[string]interface{}
	for _, tr := range rawTrackers {
		if !strings.HasPrefix(tr["url"].(string), "** [") {
			real = append(real, tr)
		}
	}
	sort.SliceStable(real, func(i, j int) bool { return num(real[i], "tier") < num(real[j], "tier") })
	trackers, _ := got["trackers"].([]interface{})
	stats, _ := got["trackerStats"].([]interface{})
	if len(trackers) != len(real) || len(stats) != len(real) {
		t.Fatalf("%s: %d trackers, %d trackerStats, want %d", hash, len(trackers), len(stats), len(real))
	}
	var tiers []string
	for i, rt := range real {
		url := rt["url"].(string)
		tr := trackers[i].(map[string]interface{})
		st := stats[i].(map[string]interface{})
		if tr["announce"] != url || tr["tier"] != num(rt, "tier") || tr["id"] != float64(i) || st["id"] != float64(i) {
			t.Errorf("%s tracker %d: %v", hash, i, tr)
		}
		if st["announce"] != url || st["host"] != expectedTrackerHost(url) || st["tier"] != num(rt, "tier") ||
			st["seederCount"] != num(rt, "num_seeds") || st["leecherCount"] != num(rt, "num_leeches") ||
			st["downloadCount"] != num(rt, "num_downloaded") || st["sitename"] != tr["sitename"] {
			t.Errorf("%s trackerStats %d: %v", hash, i, st)
		}
		if strings.HasSuffix(url, "/announce") && st["scrape"] != strings.TrimSuffix(url, "announce")+"scrape" {
			t.Errorf("%s trackerStats %d: scrape %v", hash, i, st["scrape"])
		}
		status := num(rt, "status")
		updating, _ := rt["updating"].(bool)
		switch {
		case status == 3 || updating:
			if st["announceState"] != 3.0 {
				t.Errorf("%s tracker %d (status %v): announceState %v, want 3", hash, i, status, st["announceState"])
			}
		case status == 2:
			if st["hasAnnounced"] != true || st["lastAnnounceSucceeded"] != true {
				t.Errorf("%s tracker %d working: %v", hash, i, st)
			}
		case status >= 4:
			if st["hasAnnounced"] != true || st["lastAnnounceSucceeded"] != false || st["lastAnnounceResult"] == "" {
				t.Errorf("%s tracker %d not working: %v", hash, i, st)
			}
		case status == 1:
			if st["hasAnnounced"] != false {
				t.Errorf("%s tracker %d not contacted: %v", hash, i, st)
			}
		}
		if i == 0 || num(real[i-1], "tier") != num(rt, "tier") {
			tiers = append(tiers, url)
		} else {
			tiers[len(tiers)-1] += "\n" + url
		}
	}
	if want := strings.Join(tiers, "\n\n"); got["trackerList"] != want {
		t.Errorf("%s: trackerList %q, want %q", hash, got["trackerList"], want)
	}
}

func checkDetailsPieces(t *testing.T, hash string, rawPieces []float64, got map[string]interface{}) {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(got["pieces"].(string))
	if err != nil {
		t.Fatalf("%s: pieces not base64: %v", hash, err)
	}
	if want := (len(rawPieces) + 7) / 8; len(data) != want {
		t.Errorf("%s: pieces %d bytes, want %d for %d pieces", hash, len(data), want, len(rawPieces))
	}
	have, set := 0, 0
	for _, b := range data {
		set += bits.OnesCount8(b)
	}
	for i, s := range rawPieces {
		if s == 2 {
			have++
		}
		if i/8 < len(data) && (data[i/8]>>(7-uint(i%8))&1 == 1) != (s == 2) {
			t.Fatalf("%s: piece %d bit doesn't match state %v (MSB-first)", hash, i, s)
		}
	}
	if set != have {
		t.Errorf("%s: %d bits set, want %d pieces with state 2", hash, set, have)
	}
}

func checkDetailsPeers(t *testing.T, hash, prefix string, recorded bool, got map[string]interface{}) {
	t.Helper()
	var rawResp struct {
		Peers map[string]map[string]interface{}
	}
	if !recorded || !readJSONIfExists(t, prefix+"peers.json", &rawResp) {
		if err := jsonUnmarshalString(metaDLPeers, &rawResp); err != nil {
			t.Fatal(err)
		}
	}
	peers, _ := got["peers"].([]interface{})
	if len(peers) != len(rawResp.Peers) {
		t.Fatalf("%s: %d peers, want %d", hash, len(peers), len(rawResp.Peers))
	}
	from, ok := got["peersFrom"].(map[string]interface{})
	if !ok || len(from) != len(peersFromKeys) {
		t.Fatalf("%s: peersFrom = %v", hash, got["peersFrom"])
	}
	total := 0.0
	for _, k := range peersFromKeys {
		v, ok := from[k].(float64)
		if !ok {
			t.Errorf("%s: peersFrom.%s = %v", hash, k, from[k])
		}
		total += v
	}
	if total != float64(len(peers)) {
		t.Errorf("%s: peersFrom sums to %v, want %d", hash, total, len(peers))
	}
	byAddr := map[string]map[string]interface{}{}
	for _, p := range peers {
		pm := p.(map[string]interface{})
		byAddr[fmt.Sprintf("%v:%v", pm["address"], pm["port"])] = pm
	}
	incoming := 0.0
	for _, rp := range rawResp.Peers {
		key := fmt.Sprintf("%v:%v", rp["ip"], rp["port"])
		p, ok := byAddr[key]
		if !ok {
			t.Errorf("%s: peer %d missing", hash, len(byAddr))
			continue
		}
		flags := " " + rp["flags"].(string) + " "
		has := func(f string) bool { return strings.Contains(flags, " "+f+" ") }
		if has("I") {
			incoming++
		}
		flagStr, _ := p["flagStr"].(string)
		if p["isUTP"] != has("P") || p["isIncoming"] != has("I") || p["isEncrypted"] != (has("E") || has("e")) ||
			p["isDownloadingFrom"] != has("D") || p["isUploadingTo"] != has("U") ||
			p["clientIsInterested"] != (has("D") || has("d")) || p["peerIsInterested"] != (has("U") || has("u")) ||
			strings.Contains(flagStr, "T") != has("P") || strings.Contains(flagStr, "I") != has("I") ||
			strings.Contains(flagStr, " ") ||
			p["rateToClient"] != num(rp, "dl_speed") || p["rateToPeer"] != num(rp, "up_speed") ||
			p["progress"] != num(rp, "progress") {
			t.Errorf("%s: peer with qBT flags %q mapped to %v", hash, rp["flags"], p)
		}
	}
	if from["fromIncoming"] != incoming {
		t.Errorf("%s: fromIncoming %v, want %v", hash, from["fromIncoming"], incoming)
	}
}

func jsonUnmarshalString(s string, dst interface{}) error {
	return json.Unmarshal([]byte(s), dst)
}

// TestDetailsEndpointErrors: a 409 (no metadata) on files/pieceStates gives
// empty values, a failing webseeds call an empty list, and any other endpoint
// error an error result without breaking the bridge.
func TestDetailsEndpointErrors(t *testing.T) {
	c := setUpSynthetic(t)
	byHash := func(path, hash string) *gock.Request {
		return gock.New(testQBTAddr).Get("/api/v2/"+path).MatchParam("hash", "^"+hash+"$").Persist()
	}
	byHash("torrents/properties", hashA).Reply(200).JSON(map[string]interface{}{"pieces_num": 0, "piece_size": 0, "comment": "c"})
	byHash("torrents/trackers", hashA).Reply(200).BodyString(`[]`)
	byHash("torrents/files", hashA).Reply(409).BodyString("Torrent's metadata has not yet downloaded")
	byHash("torrents/pieceStates", hashA).Reply(409).BodyString("Torrent's metadata has not yet downloaded")
	byHash("sync/torrentPeers", hashA).Reply(200).BodyString(`{"peers":{}}`)
	byHash("torrents/webseeds", hashA).Reply(500)
	byHash("torrents/trackers", hashB).Reply(500)

	out := c.mustSucceed("torrent-get", map[string]interface{}{"ids": []string{hashA}, "fields": trguingTorrentAllFields})
	got := out["torrents"].([]interface{})[0].(map[string]interface{})
	for _, f := range []string{"files", "fileStats", "priorities", "wanted", "webseeds", "trackers", "trackerStats", "peers"} {
		if l, ok := got[f].([]interface{}); !ok || len(l) != 0 {
			t.Errorf("%s = %v, want []", f, got[f])
		}
	}
	if got["pieces"] != "" || got["comment"] != "c" || got["primary-mime-type"] != "application/octet-stream" {
		t.Errorf("pieces %q comment %v mime %v", got["pieces"], got["comment"], got["primary-mime-type"])
	}
	if pf, ok := got["peersFrom"].(map[string]interface{}); !ok || len(pf) != len(peersFromKeys) {
		t.Errorf("peersFrom = %v", got["peersFrom"])
	}

	if bad := c.call("torrent-get", map[string]interface{}{"ids": []string{hashB}, "fields": []string{"id", "trackerStats"}}); bad["result"] == "success" {
		t.Error("trackers HTTP 500: expected an error result")
	}
	c.mustSucceed("torrent-get", map[string]interface{}{"fields": []string{"id"}})
}

func TestPiecesBitfield(t *testing.T) {
	for _, tc := range []struct {
		states []byte
		want   []byte
	}{
		{nil, []byte{}},
		{[]byte{2, 0, 1, 2, 2, 0, 0, 0}, []byte{0x98}},             // 8 pieces: one byte (h31 PR #6 sends two)
		{[]byte{2, 0, 1, 2, 2, 0, 0, 0, 2}, []byte{0x98, 0x80}},    // MSB first, last byte padded with 0
		{[]byte{1, 1, 1, 1, 1, 1, 1, 1, 1, 1}, []byte{0x00, 0x00}}, // downloading = missing
	} {
		if got := piecesBitfield(tc.states); got != base64.StdEncoding.EncodeToString(tc.want) {
			t.Errorf("piecesBitfield(%v) = %q, want %x", tc.states, got, tc.want)
		}
	}
}

func TestMapPeerFlags(t *testing.T) {
	type flags struct{ cChoked, cInt, pChoked, pInt, down, up bool }
	for _, tc := range []struct {
		qbt, flagStr, from string
		want               flags
	}{
		{"D I H E P", "TDEI", "fromIncoming", flags{cChoked: false, cInt: true, pChoked: true, down: true}},
		{"U H X P", "TUH", "fromDht", flags{cChoked: true, pInt: true, up: true}},
		{"d ? X P", "Td?X", "fromPex", flags{cChoked: true, cInt: true}},
		{"K ? L e", "K?E", "fromLpd", flags{}},
		{"O u S", "Ou", "fromTracker", flags{cChoked: true, pChoked: true, pInt: true}},
		{"", "", "fromTracker", flags{cChoked: true, pChoked: true}},
	} {
		p, from := mapPeer(qBT.PeerInfo{Flags: tc.qbt})
		got := flags{p.ClientIsChoked, p.ClientIsInterested, p.PeerIsChoked, p.PeerIsInterested, p.IsDownloadingFrom, p.IsUploadingTo}
		if got != tc.want || p.FlagStr != tc.flagStr || from != tc.from {
			t.Errorf("flags %q: %+v %q %s, want %+v %q %s", tc.qbt, got, p.FlagStr, from, tc.want, tc.flagStr, tc.from)
		}
	}
	if p, _ := mapPeer(qBT.PeerInfo{I2p_dest: "dest.b32.i2p"}); p.Address != "dest.b32.i2p" {
		t.Errorf("I2P address = %q", p.Address)
	}
}

func TestFilePriorityMapping(t *testing.T) {
	for qbt, want := range expectedFilePriority {
		if got := qBTFilePriorityToTransmission(int(qbt)); float64(got) != want {
			t.Errorf("qBT priority %v -> %d, want %v", qbt, got, want)
		}
	}
	if got := fileBytesCompleted(1000, 0.9999999); got != 999 {
		t.Errorf("almost complete file: %d bytes, want 999", got)
	}
}

func TestPrimaryMimeType(t *testing.T) {
	f := func(name string, size int64) qBT.PropertiesFiles { return qBT.PropertiesFiles{Name: name, Size: size} }
	for _, tc := range []struct {
		files []qBT.PropertiesFiles
		want  string
	}{
		{nil, "application/octet-stream"},
		{[]qBT.PropertiesFiles{f("T/a.mkv", 10), f("T/a.srt", 1), f("T/b.srt", 1)}, "video/x-matroska"},
		{[]qBT.PropertiesFiles{f("T.MP4", 5)}, "video/mp4"},
		{[]qBT.PropertiesFiles{f("T/x.zzqq", 50), f("T/y.iso", 40)}, "application/octet-stream"},
		{[]qBT.PropertiesFiles{f("T/noext", 1)}, "application/octet-stream"},
	} {
		if got := primaryMimeType(tc.files); got != tc.want {
			t.Errorf("primaryMimeType(%v) = %q, want %q", tc.files, got, tc.want)
		}
	}
}

func TestScrapeURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://t.example/announce":            "http://t.example/scrape",
		"https://t.example/x/announce.php?k=v": "https://t.example/x/scrape.php?k=v",
		"udp://t.example:80":                   "udp://t.example:80",
		"http://t.example/a":                   "",
	} {
		if got := scrapeURL(in); got != want {
			t.Errorf("scrapeURL(%q) = %q, want %q", in, got, want)
		}
	}
}
