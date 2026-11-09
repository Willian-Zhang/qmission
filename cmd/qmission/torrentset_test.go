package main

import (
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/Willian-Zhang/qmission/qBT"
	"gopkg.in/h2non/gock.v1"
)

// Every qBT endpoint torrent-set may write to.
var torrentSetWritePaths = []string{
	"filePrio", "setDownloadLimit", "setUploadLimit", "setShareLimits", "addTags", "removeTags",
	"addTrackers", "removeTrackers", "editTracker", "toggleSequentialDownload", "toggleFirstLastPiecePrio",
	"setLocation", "topPrio", "bottomPrio", "increasePrio", "decreasePrio",
}

type tsCall struct {
	path string
	form url.Values
}

func (c tsCall) String() string { return c.path + " " + c.form.Encode() }

type tsFixture struct {
	t      *testing.T
	ds     *dataset
	c      *rpcClient
	writes map[string]*captured
	byID   map[float64]string
	raw    map[string]map[string]interface{} // hash -> merged maindata fields
}

// runTorrentSet runs fn per dataset with every write endpoint captured (200)
// and the sync chain drained, so the table matches the last recording.
func runTorrentSet(t *testing.T, pre func(t *testing.T, ds *dataset), fn func(t *testing.T, f *tsFixture)) {
	var writes map[string]*captured
	forEachDatasetWith(t, func(t *testing.T, ds *dataset) {
		if pre != nil { // first, so its mocks win over the captures
			pre(t, ds)
		}
		writes = map[string]*captured{}
		for _, p := range torrentSetWritePaths {
			writes[p] = capturePost(t, "/api/v2/torrents/"+p+"$", 200, "")
		}
	}, func(t *testing.T, ds *dataset, c *rpcClient) {
		_, byID := drainSync(t, ds, c)
		fn(t, &tsFixture{t: t, ds: ds, c: c, writes: writes, byID: byID, raw: ds.torrentsAfter(len(ds.syncFiles) - 1)})
	})
}

// set sends torrent-set and returns its result and the qBT writes it made.
func (f *tsFixture) set(args map[string]interface{}) (string, []tsCall) {
	f.t.Helper()
	before := map[string]int{}
	for p, w := range f.writes {
		before[p] = w.count()
	}
	out := f.c.call("torrent-set", args)
	var calls []tsCall
	for _, p := range torrentSetWritePaths {
		for _, form := range f.writes[p].all()[before[p]:] {
			calls = append(calls, tsCall{p, form})
		}
	}
	result, _ := out["result"].(string)
	return result, calls
}

func (f *tsFixture) mustSet(args map[string]interface{}, want ...tsCall) {
	f.t.Helper()
	result, calls := f.set(args)
	if result != "success" {
		f.t.Fatalf("torrent-set %v: result %q", args, result)
	}
	checkCalls(f.t, calls, want)
}

func (f *tsFixture) mustFail(args map[string]interface{}, contains string) {
	f.t.Helper()
	result, calls := f.set(args)
	if result == "success" || !strings.Contains(result, contains) {
		f.t.Errorf("torrent-set %v: result %q, want an error containing %q", args, result, contains)
	}
	if len(calls) != 0 {
		f.t.Errorf("failed torrent-set still wrote: %v", calls)
	}
}

func checkCalls(t *testing.T, got, want []tsCall) {
	t.Helper()
	sortCalls := func(c []tsCall) []string {
		s := make([]string, len(c))
		for i := range c {
			s[i] = c[i].String()
		}
		sort.Strings(s)
		return s
	}
	if g, w := sortCalls(got), sortCalls(want); !reflect.DeepEqual(g, w) && (len(g) != 0 || len(w) != 0) {
		t.Errorf("qBT writes:\n got  %q\n want %q", g, w)
	}
}

// pick returns the lowest ID whose torrent has recorded details (props etc.)
// and matches ok.
func (f *tsFixture) pick(ok func(raw map[string]interface{}) bool) (float64, string) {
	f.t.Helper()
	ids := make([]float64, 0, len(f.byID))
	for id := range f.byID {
		ids = append(ids, id)
	}
	sort.Float64s(ids)
	for _, id := range ids {
		hash := f.byID[id]
		if _, has := f.ds.props[hash]; has && (ok == nil || ok(f.raw[hash])) {
			return id, hash
		}
	}
	f.t.Skip("no matching torrent in this recording")
	return 0, ""
}

func hasMetadata(raw map[string]interface{}) bool {
	s, _ := raw["state"].(string)
	return s != "metaDL" && s != "forcedMetaDL"
}

// details is TrguiNG's details torrent-get (one id), limited to what the edit dialogs read.
func (f *tsFixture) details(id float64) map[string]interface{} {
	f.t.Helper()
	out := f.c.mustSucceed("torrent-get", map[string]interface{}{"ids": []interface{}{id}, "fields": []string{
		"id", "downloadLimited", "downloadLimit", "uploadLimited", "uploadLimit", "peer-limit",
		"seedRatioMode", "seedRatioLimit", "seedIdleMode", "seedIdleLimit", "honorsSessionLimits",
		"sequential_download", "labels", "trackerList", "trackers", "queuePosition", "downloadDir", "bandwidthPriority",
	}})
	return out["torrents"].([]interface{})[0].(map[string]interface{})
}

// editForm is TrguiNG's Edit Torrent payload (modals/edittorrent.tsx onSave):
// the form spread as is (camelCase peerLimit/sequentialDownload) plus
// "peer-limit" and sequential_download, sent to every selected torrent.
func editForm(d map[string]interface{}, ids ...float64) map[string]interface{} {
	return map[string]interface{}{
		"downloadLimited":     d["downloadLimited"],
		"downloadLimit":       d["downloadLimit"],
		"uploadLimited":       d["uploadLimited"],
		"uploadLimit":         d["uploadLimit"],
		"peerLimit":           d["peer-limit"],
		"seedRatioMode":       d["seedRatioMode"],
		"seedRatioLimit":      d["seedRatioLimit"],
		"seedIdleMode":        d["seedIdleMode"],
		"seedIdleLimit":       d["seedIdleLimit"],
		"honorsSessionLimits": d["honorsSessionLimits"],
		"sequentialDownload":  d["sequential_download"],
		"peer-limit":          d["peer-limit"],
		"sequential_download": d["sequential_download"],
		"ids":                 ids,
	}
}

func withKV(m map[string]interface{}, kv ...interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	for k, v := range m {
		out[k] = v
	}
	for i := 0; i < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}

func (f *tsFixture) webapi() qBT.APIVersion {
	v, err := qBT.ParseAPIVersion(f.ds.webapi)
	if err != nil {
		f.t.Fatal(err)
	}
	return v
}

// shareLimitsForm is the setShareLimits form expected for a torrent, written
// from the WebAPI changelog independently of the code under test.
func (f *tsFixture) shareLimitsForm(hash string, ratio string, idle string) url.Values {
	raw := f.raw[hash]
	seeding := "-2"
	if v, ok := raw["seeding_time_limit"].(float64); ok {
		seeding = strconv.Itoa(int(v))
	}
	form := url.Values{"hashes": {hash}, "ratioLimit": {ratio}, "seedingTimeLimit": {seeding}}
	v := f.webapi()
	if v.AtLeast(qBT.APIVersion{Major: 2, Minor: 9, Patch: 2}) {
		form.Set("inactiveSeedingTimeLimit", idle)
	}
	str := func(key string) string {
		if s, _ := raw[key].(string); s != "" {
			return s
		}
		return "Default"
	}
	if v.AtLeast(qBT.APIVersion{Major: 2, Minor: 12}) {
		form.Set("shareLimitAction", str("share_limit_action"))
	}
	if v.AtLeast(qBT.APIVersion{Major: 2, Minor: 15, Patch: 3}) {
		form.Set("shareLimitsMode", str("share_limits_mode"))
	}
	return form
}

func fmtNum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// TestTorrentSetEditUnchanged: TrguiNG's Edit Torrent payload with nothing
// changed succeeds without a single qBT write, for every torrent with details.
func TestTorrentSetEditUnchanged(t *testing.T) {
	runTorrentSet(t, nil, func(t *testing.T, f *tsFixture) {
		n := 0
		for id, hash := range f.byID {
			if _, ok := f.ds.props[hash]; !ok {
				continue
			}
			n++
			d := f.details(id)
			f.mustSet(editForm(d, id))
			// The toolbar's "normal" bandwidth priority and an unchanged labels/trackerList too.
			f.mustSet(map[string]interface{}{"ids": []float64{id}, "bandwidthPriority": 0})
			f.mustSet(map[string]interface{}{"ids": []float64{id}, "labels": d["labels"], "trackerList": d["trackerList"]})
		}
		if n == 0 {
			t.Fatal("no torrent with details in the recording")
		}
	})
}

// TestTorrentSetFieldAlone: each Edit Torrent field changed alone makes exactly the expected qBT calls.
func TestTorrentSetFieldAlone(t *testing.T) {
	runTorrentSet(t, nil, func(t *testing.T, f *tsFixture) {
		id, hash := f.pick(hasMetadata)
		raw := f.raw[hash]
		d := f.details(id)
		form := editForm(d, id)
		h := url.Values{"hashes": {hash}}
		hashesWith := func(kv ...string) url.Values {
			v := url.Values{"hashes": {hash}}
			for i := 0; i < len(kv); i += 2 {
				v.Set(kv[i], kv[i+1])
			}
			return v
		}

		dl := d["downloadLimit"].(float64)
		f.mustSet(withKV(form, "downloadLimited", true, "downloadLimit", dl+50),
			tsCall{"setDownloadLimit", hashesWith("limit", strconv.Itoa(int(dl+50)*1024))})
		if d["downloadLimited"] == true {
			f.mustSet(withKV(form, "downloadLimited", false), tsCall{"setDownloadLimit", hashesWith("limit", "0")})
		} else {
			f.mustSet(withKV(form, "downloadLimited", true, "downloadLimit", 100), tsCall{"setDownloadLimit", hashesWith("limit", "102400")})
		}
		if d["uploadLimited"] == true {
			f.mustSet(withKV(form, "uploadLimited", false), tsCall{"setUploadLimit", hashesWith("limit", "0")})
		} else {
			f.mustSet(withKV(form, "uploadLimited", true, "uploadLimit", 20), tsCall{"setUploadLimit", hashesWith("limit", "20480")})
		}

		idle := "-2"
		if v, ok := raw["inactive_seeding_time_limit"].(float64); ok {
			idle = strconv.Itoa(int(v))
		}
		ratio := fmtNum(num(raw, "ratio_limit"))
		f.mustSet(withKV(form, "seedRatioMode", 2), tsCall{"setShareLimits", f.shareLimitsForm(hash, "-1", idle)})
		f.mustSet(withKV(form, "seedRatioMode", 1, "seedRatioLimit", 2.5), tsCall{"setShareLimits", f.shareLimitsForm(hash, "2.5", idle)})
		f.mustSet(withKV(form, "seedIdleMode", 1, "seedIdleLimit", 30), tsCall{"setShareLimits", f.shareLimitsForm(hash, ratio, "30")})
		f.mustSet(withKV(form, "seedIdleMode", 2), tsCall{"setShareLimits", f.shareLimitsForm(hash, ratio, "-1")})

		// sequential_download toggles seq_dl, and first/last-piece priority only where it differs.
		seq, _ := raw["seq_dl"].(bool)
		firstLast, _ := raw["f_l_piece_prio"].(bool)
		want := []tsCall{{"toggleSequentialDownload", h}}
		if firstLast == seq { // the target (!seq) differs
			want = append(want, tsCall{"toggleFirstLastPiecePrio", h})
		}
		f.mustSet(withKV(form, "sequential_download", !seq, "sequentialDownload", !seq), want...)

		// Junk camelCase keys alone are ignored.
		f.mustSet(withKV(form, "peerLimit", 12345, "sequentialDownload", !seq))
	})
}

// TestTorrentSetUnmappable: a changed field qBT can't store fails the request
// and nothing is written, even with mappable changes in the same request.
func TestTorrentSetUnmappable(t *testing.T) {
	runTorrentSet(t, nil, func(t *testing.T, f *tsFixture) {
		id, _ := f.pick(hasMetadata)
		d := f.details(id)
		form := editForm(d, id)
		f.mustFail(withKV(form, "peer-limit", d["peer-limit"].(float64)+7), "peer-limit")
		f.mustFail(withKV(form, "honorsSessionLimits", false), "honorsSessionLimits")
		f.mustFail(withKV(form, "peer-limit", 3, "downloadLimited", true, "downloadLimit", 777, "seedRatioMode", 2), "peer-limit")
		f.mustFail(map[string]interface{}{"ids": []float64{id}, "bandwidthPriority": 1}, "bandwidthPriority")
		f.mustFail(map[string]interface{}{"ids": []float64{id}, "group": "g1"}, "group")
		f.mustFail(map[string]interface{}{"ids": []float64{id}, "sequential_download_from_piece": 5}, "sequential_download_from_piece")
		// Values qBT can't hold.
		f.mustFail(withKV(form, "downloadLimited", true, "downloadLimit", 0), "downloadLimit")
		f.mustFail(withKV(form, "seedRatioMode", 7), "seedRatioMode")
		f.mustFail(map[string]interface{}{"ids": []float64{id}, "labels": []string{"a,b"}}, "comma")
		f.mustFail(map[string]interface{}{"ids": []float64{id}, "labels": []string{"qmission-add-0123456789abcdef"}}, "reserved")
		f.mustFail(map[string]interface{}{"ids": []float64{id}, "location": " "}, "location")
	})
}

// TestTorrentSetMultiEdit: Edit Torrent on several torrents fills the form from
// one of them; a field equal to any targeted torrent's value is unchanged, and
// a changed field goes to all of them in one call.
func TestTorrentSetMultiEdit(t *testing.T) {
	runTorrentSet(t, nil, func(t *testing.T, f *tsFixture) {
		// Torrents without recorded props get a synthetic properties answer.
		gock.New(testQBTAddr).Get("/api/v2/torrents/properties").Persist().Reply(200).BodyString(`{"nb_connections_limit": 100}`)
		idA, hashA := f.pick(hasMetadata)
		var ids []float64
		for id := range f.byID {
			ids = append(ids, id)
		}
		sort.Float64s(ids)
		if len(ids) < 2 {
			t.Skip("need two torrents")
		}
		d := f.details(idA)
		f.mustSet(editForm(d, ids...))

		// A value taken from another torrent counts as unchanged too.
		var other float64 = -1
		for _, id := range ids {
			if id != idA && num(f.raw[f.byID[id]], "dl_limit") != num(f.raw[hashA], "dl_limit") {
				other = id
			}
		}
		if other >= 0 {
			o := f.c.mustSucceed("torrent-get", map[string]interface{}{"ids": []interface{}{other},
				"fields": []string{"downloadLimit", "downloadLimited"}})["torrents"].([]interface{})[0].(map[string]interface{})
			f.mustSet(withKV(editForm(d, ids...), "downloadLimit", o["downloadLimit"], "downloadLimited", o["downloadLimited"]))
		}

		all := make([]string, len(ids))
		for i, id := range ids {
			all[i] = f.byID[id]
		}
		f.mustSet(withKV(editForm(d, ids...), "uploadLimited", true, "uploadLimit", 33),
			tsCall{"setUploadLimit", url.Values{"hashes": {strings.Join(all, "|")}, "limit": {"33792"}}})
	})
}

// TestTorrentSetFilePriorities: file commands are always applied, one filePrio
// call per resulting qBT priority (0 unwanted, 1 low, 6 normal, 7 high).
func TestTorrentSetFilePriorities(t *testing.T) {
	runTorrentSet(t, nil, func(t *testing.T, f *tsFixture) {
		id, hash := f.pick(hasMetadata)
		var files []map[string]interface{}
		readJSONFile(t, f.ds.dir+"/torrent_"+strings.TrimLeft(hash, "0")+"_files.json", &files)
		if len(files) < 5 {
			t.Skipf("torrent has %d files", len(files))
		}
		prio := func(i int) int { return int(files[i]["priority"].(float64)) }
		// Expected, from the rules: unwanted 0; wanted keeps a nonzero
		// priority, else 6; a priority on a file that stays unwanted stays 0.
		want := map[int][]int{}
		put := func(v, file int) { want[v] = append(want[v], file) }
		keep := func(i, p int) int {
			if prio(i) == 0 {
				return 0
			}
			return p
		}
		put(keep(0, 7), 0)
		put(keep(1, 7), 1)
		put(keep(2, 1), 2)
		if prio(3) != 0 {
			put(prio(3), 3)
		} else {
			put(6, 3)
		}
		put(0, 4)
		var calls []tsCall
		for v, ids := range want {
			s := make([]string, len(ids))
			for i, id := range ids {
				s[i] = strconv.Itoa(id)
			}
			calls = append(calls, tsCall{"filePrio", url.Values{"hash": {hash}, "id": {strings.Join(s, "|")}, "priority": {strconv.Itoa(v)}}})
		}
		f.mustSet(map[string]interface{}{"ids": []float64{id},
			"priority-high": []int{0, 1}, "priority-low": []int{2}, "files-wanted": []int{3}, "files-unwanted": []int{4}}, calls...)

		// Wanted after unwanted wins, like libtransmission; empty array = all files.
		allIDs := make([]string, len(files))
		for i := range files {
			allIDs[i] = strconv.Itoa(i)
		}
		f.mustSet(map[string]interface{}{"ids": []float64{id}, "files-unwanted": []int{}},
			tsCall{"filePrio", url.Values{"hash": {hash}, "id": {strings.Join(allIDs, "|")}, "priority": {"0"}}})
		wantedPrio := 6
		if prio(1) != 0 {
			wantedPrio = prio(1)
		}
		f.mustSet(map[string]interface{}{"ids": []float64{id}, "files-unwanted": []int{1}, "files-wanted": []int{1}},
			tsCall{"filePrio", url.Values{"hash": {hash}, "id": {"1"}, "priority": {strconv.Itoa(wantedPrio)}}})

		f.mustFail(map[string]interface{}{"ids": []float64{id}, "priority-high": []int{len(files)}}, "out of range")
		f.mustFail(map[string]interface{}{"ids": []float64{id}, "priority-high": []interface{}{"x"}}, "priority-high")

		// A torrent still fetching metadata gets a clear error and no call.
		for mid, mhash := range f.byID {
			if !hasMetadata(f.raw[mhash]) {
				f.mustFail(map[string]interface{}{"ids": []float64{mid}, "files-wanted": []int{0}}, "no metadata")
			}
		}
	})
}

// addMarkerDelta makes the next sync poll (rid = last) tag torrent `hash` with
// a label plus a torrent-add marker.
func addMarkerDelta(t *testing.T, ds *dataset, hashOf func(ds *dataset) string) {
	hash := hashOf(ds)
	gock.New(testQBTAddr).Get("/api/v2/sync/maindata").MatchParam("rid", fmt.Sprintf("^%d$", ds.lastRid())).
		Reply(200).BodyString(fmt.Sprintf(`{"rid": %d, "torrents": {%q: {"tags": "alpha, qmission-add-0123456789abcdef"}}}`, ds.lastRid(), hash))
}

// TestTorrentSetLabels: labels are diffed against qBT's tags; torrent-add's
// marker tags are never removed and order doesn't matter.
func TestTorrentSetLabels(t *testing.T) {
	var marked string
	runTorrentSet(t, func(t *testing.T, ds *dataset) {
		addMarkerDelta(t, ds, func(ds *dataset) string {
			final := ds.torrentsAfter(len(ds.syncFiles) - 1)
			hashes := make([]string, 0, len(final))
			for h := range final {
				hashes = append(hashes, h)
			}
			sort.Strings(hashes)
			marked = hashes[0]
			return marked
		})
	}, func(t *testing.T, f *tsFixture) {
		var id float64
		for i, h := range f.byID {
			if h == marked {
				id = i
			}
		}
		withTags := func(tags string) url.Values { return url.Values{"hashes": {marked}, "tags": {tags}} }
		f.mustSet(map[string]interface{}{"ids": []float64{id}, "labels": []string{"beta", "alpha"}}, tsCall{"addTags", withTags("beta")})
		f.mustSet(map[string]interface{}{"ids": []float64{id}, "labels": []string{" alpha ", "alpha"}})
		f.mustSet(map[string]interface{}{"ids": []float64{id}, "labels": []string{}}, tsCall{"removeTags", withTags("alpha")})
		f.mustSet(map[string]interface{}{"ids": []float64{id}, "labels": []string{"gamma"}},
			tsCall{"removeTags", withTags("alpha")}, tsCall{"addTags", withTags("gamma")})
		got := f.c.mustSucceed("torrent-get", map[string]interface{}{"ids": []float64{id}, "fields": []string{"labels"}})
		if l := got["torrents"].([]interface{})[0].(map[string]interface{})["labels"]; !reflect.DeepEqual(l, []interface{}{"alpha"}) {
			t.Errorf("labels = %v (marker must stay hidden)", l)
		}
	})
}

// TestTorrentSetIdleLimitGate: below WebAPI 2.9.2 (qBT 4.6.0) there is no
// inactive seeding limit: changing seedIdle* fails, and setShareLimits leaves
// inactiveSeedingTimeLimit out.
func TestTorrentSetIdleLimitGate(t *testing.T) {
	runTorrentSet(t, func(t *testing.T, ds *dataset) { ds.webapi = "2.8.19" }, func(t *testing.T, f *tsFixture) {
		id, hash := f.pick(hasMetadata)
		d := f.details(id)
		form := editForm(d, id)
		f.mustSet(form)
		f.mustFail(withKV(form, "seedIdleMode", 1, "seedIdleLimit", 30), "seedIdle")
		f.mustSet(withKV(form, "seedRatioMode", 2), tsCall{"setShareLimits", f.shareLimitsForm(hash, "-1", "")})
		if f.writes["setShareLimits"].all()[0].Has("inactiveSeedingTimeLimit") {
			t.Error("inactiveSeedingTimeLimit sent below WebAPI 2.9.2")
		}
	})
}

// TestTorrentSetTrackerList: trackerList is diffed into removeTrackers /
// addTrackers; a new tier at the end works, tier moves are refused.
func TestTorrentSetTrackerList(t *testing.T) {
	runTorrentSet(t, nil, func(t *testing.T, f *tsFixture) {
		id, hash := f.pick(func(raw map[string]interface{}) bool { return hasMetadata(raw) && num(raw, "trackers_count") > 0 })
		d := f.details(id)
		list := d["trackerList"].(string)
		tiers := parseTrackerTiers(list)
		if len(tiers) == 0 {
			t.Skip("no trackers")
		}
		var maxTier float64 = -1
		var first string
		for i, tr := range d["trackers"].([]interface{}) {
			m := tr.(map[string]interface{})
			if tier := m["tier"].(float64); tier > maxTier {
				maxTier = tier
			}
			if i == 0 {
				first = m["announce"].(string)
			}
		}
		const extra = "udp://new.example:6969/announce"
		ids := []float64{id}
		// TrguiNG's dedupe can leave a trailing newline; that's no change.
		f.mustSet(map[string]interface{}{"ids": ids, "trackerList": list + "\n"})
		f.mustSet(map[string]interface{}{"ids": ids, "trackerList": list + "\n\n" + extra},
			tsCall{"addTrackers", url.Values{"hash": {hash}, "urls": {strings.Repeat("\n", int(maxTier)+1) + extra}}})
		rest := strings.TrimPrefix(strings.TrimPrefix(list, first), "\n")
		f.mustSet(map[string]interface{}{"ids": ids, "trackerList": rest},
			tsCall{"removeTrackers", url.Values{"hash": {hash}, "urls": {first}}})
		f.mustFail(map[string]interface{}{"ids": ids, "trackerList": extra + "\n\n" + list}, "trackerList")
		if len(tiers) > 1 {
			f.mustFail(map[string]interface{}{"ids": ids, "trackerList": strings.ReplaceAll(list, "\n\n", "\n")}, "trackerList")
		}
		// Deprecated commands.
		f.mustSet(map[string]interface{}{"ids": ids, "trackerAdd": []string{extra}},
			tsCall{"addTrackers", url.Values{"hash": {hash}, "urls": {strings.Repeat("\n", int(maxTier)+1) + extra}}})
		f.mustSet(map[string]interface{}{"ids": ids, "trackerRemove": []int{0}},
			tsCall{"removeTrackers", url.Values{"hash": {hash}, "urls": {first}}})
		f.mustSet(map[string]interface{}{"ids": ids, "trackerReplace": []interface{}{0, extra}},
			tsCall{"editTracker", url.Values{"hash": {hash}, "origUrl": {first}, "url": {first}, "newUrl": {extra}}})
		f.mustFail(map[string]interface{}{"ids": ids, "trackerRemove": []int{999}}, "no tracker id")
	})
}

// TestTorrentSetLocationAndQueue: location → setLocation; queuePosition moves
// a torrent in qBT's queue and fails for torrents outside it.
func TestTorrentSetLocationAndQueue(t *testing.T) {
	runTorrentSet(t, nil, func(t *testing.T, f *tsFixture) {
		ids := make([]float64, 0, len(f.byID))
		for id := range f.byID {
			ids = append(ids, id)
		}
		sort.Float64s(ids)
		first := ids[0]
		hash := f.byID[first]
		f.mustSet(map[string]interface{}{"ids": []float64{first}, "location": f.raw[hash]["save_path"]})
		f.mustSet(map[string]interface{}{"ids": []float64{first}, "location": "/elsewhere"},
			tsCall{"setLocation", url.Values{"hashes": {hash}, "location": {"/elsewhere"}}})

		queued := 0
		for _, raw := range f.raw {
			if num(raw, "priority") > 0 {
				queued++
			}
		}
		for _, id := range ids {
			h := f.byID[id]
			p := int(num(f.raw[h], "priority"))
			args := map[string]interface{}{"ids": []float64{id}, "queuePosition": 0}
			switch {
			case p <= 0:
				f.mustFail(args, "queue")
			case p == 1:
				f.mustSet(args)
			default:
				f.mustSet(args, tsCall{"topPrio", url.Values{"hashes": {h}}})
				if p < queued {
					f.mustSet(withKV(args, "queuePosition", 99), tsCall{"bottomPrio", url.Values{"hashes": {h}}})
				}
			}
		}
		// Position 0 is some torrent's current one, so it's unchanged for a multi-edit.
		f.mustSet(map[string]interface{}{"ids": ids, "queuePosition": 0})
		f.mustFail(map[string]interface{}{"ids": ids, "queuePosition": 99}, "queuePosition")
	})
}

// TestTorrentSetPartialFailure: writes stop at the first qBT error, and the
// error says earlier changes may be applied.
func TestTorrentSetPartialFailure(t *testing.T) {
	runTorrentSet(t, func(t *testing.T, ds *dataset) {
		gock.New(testQBTAddr).Post("/api/v2/torrents/setUploadLimit").Reply(500).BodyString("boom")
	}, func(t *testing.T, f *tsFixture) {
		id, _ := f.pick(hasMetadata)
		d := f.details(id)
		result, calls := f.set(withKV(editForm(d, id), "downloadLimited", true, "downloadLimit", 4321, "uploadLimited", true, "uploadLimit", 99))
		if !strings.Contains(result, "after 1 of 2") || !strings.Contains(result, "setUploadLimit") {
			t.Errorf("result %q", result)
		}
		if len(calls) != 1 || calls[0].path != "setDownloadLimit" {
			t.Errorf("calls %v", calls)
		}
	})
}

func TestParseTrackerTiers(t *testing.T) {
	got := parseTrackerTiers("\n a \n\n\nb\na\n c\n\n")
	if want := [][]string{{"a"}, {"b", "c"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("parseTrackerTiers = %q, want %q", got, want)
	}
	if s := trackerURLsString([]string{"x", "y", "z"}, []int{2, 0, 2}); s != "y\n\n\nx\nz" {
		t.Errorf("trackerURLsString = %q", s)
	}
}
