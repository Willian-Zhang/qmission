package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gopkg.in/h2non/gock.v1"
)

// sessionSetEnv is one dataset run with setPreferences and the alt-speed
// calls captured.
type sessionSetEnv struct {
	t         *testing.T
	ds        *dataset
	c         *rpcClient
	prefs     *captured
	speedMode *captured // transfer/setSpeedLimitsMode
	toggle    *captured // transfer/toggleSpeedLimitsMode (only below WebAPI 2.8.14)
}

// forEachSessionSet runs fn per dataset; prefsOverride edits the recorded preferences.
func forEachSessionSet(t *testing.T, prefsOverride func(map[string]interface{}), fn func(e *sessionSetEnv)) {
	var prefs, speedMode, toggle *captured
	forEachDatasetWith(t, func(t *testing.T, ds *dataset) {
		prefs = capturePost(t, "/api/v2/app/setPreferences", 200, "")
		speedMode = capturePost(t, "/api/v2/transfer/setSpeedLimitsMode", 200, "")
		toggle = capturePost(t, "/api/v2/transfer/toggleSpeedLimitsMode", 200, "")
		if prefsOverride != nil {
			p := map[string]interface{}{}
			readJSONFile(t, filepath.Join(ds.dir, "preferences.json"), &p)
			prefsOverride(p)
			gock.New(testQBTAddr).Get("/api/v2/app/preferences").Persist().Reply(200).JSON(p)
		}
	}, func(t *testing.T, ds *dataset, c *rpcClient) {
		fn(&sessionSetEnv{t: t, ds: ds, c: c, prefs: prefs, speedMode: speedMode, toggle: toggle})
	})
}

// form is what TrguiNG's settings dialog starts from: session-get with SessionAllFields.
func (e *sessionSetEnv) form() map[string]interface{} {
	e.t.Helper()
	return e.c.mustSucceed("session-get", map[string]interface{}{"fields": trguingSessionAllFields})
}

// set sends the form and returns the result plus the setPreferences `json`
// objects sent by this call.
func (e *sessionSetEnv) set(form map[string]interface{}) (string, []map[string]interface{}) {
	e.t.Helper()
	before := e.prefs.count()
	out := e.c.call("session-set", form)
	var sent []map[string]interface{}
	for _, f := range e.prefs.all()[before:] {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(f.Get("json")), &m); err != nil {
			e.t.Fatalf("setPreferences json: %v", err)
		}
		sent = append(sent, m)
	}
	result, _ := out["result"].(string)
	return result, sent
}

func (e *sessionSetEnv) expectPrefs(name string, form map[string]interface{}, want map[string]interface{}) {
	e.t.Helper()
	result, sent := e.set(form)
	if result != "success" {
		e.t.Errorf("%s: result = %q", name, result)
		return
	}
	if len(sent) != 1 {
		e.t.Errorf("%s: %d setPreferences calls, want 1", name, len(sent))
		return
	}
	if w := normalizeJSON(want); !reflect.DeepEqual(sent[0], w) {
		e.t.Errorf("%s: setPreferences %v, want %v", name, sent[0], w)
	}
}

func (e *sessionSetEnv) expectUnmappable(name string, form map[string]interface{}, fields ...string) {
	e.t.Helper()
	result, sent := e.set(form)
	if len(sent) != 0 {
		e.t.Errorf("%s: setPreferences called %d times on an error", name, len(sent))
	}
	sort.Strings(fields)
	want := unmappableError(fields).Error()
	if result != want {
		e.t.Errorf("%s: result = %q, want %q", name, result, want)
	}
}

func (e *sessionSetEnv) expectNoWrite(name string, form map[string]interface{}) {
	e.t.Helper()
	result, sent := e.set(form)
	if result != "success" || len(sent) != 0 {
		e.t.Errorf("%s: result %q, %d setPreferences calls; want success and none", name, result, len(sent))
	}
}

func with(form map[string]interface{}, kv ...interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(form))
	for k, v := range form {
		out[k] = v
	}
	for i := 0; i < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}

func TestSessionSetUnchangedForm(t *testing.T) {
	forEachSessionSet(t, nil, func(e *sessionSetEnv) {
		form := e.form()
		if len(form) != len(trguingSessionAllFields) {
			e.t.Fatalf("form has %d fields, want %d", len(form), len(trguingSessionAllFields))
		}
		e.expectNoWrite("unchanged", form)
		// The full session-get (no fields) round-trips too.
		e.expectNoWrite("unchanged full", e.c.mustSucceed("session-get", nil))
		// Edit-dialog junk that isn't a session key is ignored.
		e.expectNoWrite("unknown key", with(form, "peerLimit", 5, "foo", "bar"))
		if e.toggle.count()+e.speedMode.count() != 0 {
			e.t.Errorf("alt speed changed: %d toggles, %d sets", e.toggle.count(), e.speedMode.count())
		}
	})
}

func TestSessionSetReadOnlyIgnored(t *testing.T) {
	forEachSessionSet(t, nil, func(e *sessionSetEnv) {
		form := e.form()
		e.expectNoWrite("read-only", with(form,
			"version", "9.9.9", "rpc-version", 99, "rpc-version-minimum", 1, "rpc-version-semver", "9.0.0",
			"config-dir", "/elsewhere", "blocklist-size", 42, "session-id", "other",
			"units", map[string]interface{}{"speed-bytes": 1000}, "download-dir-free-space", 1, "tcp-enabled", false))
	})
}

func TestSessionSetEachField(t *testing.T) {
	forEachSessionSet(t, nil, func(e *sessionSetEnv) {
		form := e.form()
		b := func(k string) bool { return form[k].(bool) }
		n := func(k string) int { return int(form[k].(float64)) }

		bools := map[string]string{
			"alt-speed-time-enabled":       "scheduler_enabled",
			"blocklist-enabled":            "ip_filter_enabled",
			"dht-enabled":                  "dht",
			"lpd-enabled":                  "lsd",
			"pex-enabled":                  "pex",
			"port-forwarding-enabled":      "upnp",
			"incomplete-dir-enabled":       "temp_path_enabled",
			"rename-partial-files":         "incomplete_files_ext",
			"queue-stalled-enabled":        "dont_count_slow_torrents",
			"script-torrent-done-enabled":  "autorun_enabled",
			"script-torrent-added-enabled": "autorun_on_torrent_added_enabled",
		}
		for field, pref := range bools {
			e.expectPrefs(field, with(form, field, !b(field)), map[string]interface{}{pref: !b(field)})
		}
		strs := map[string]string{
			"download-dir":                  "save_path",
			"incomplete-dir":                "temp_path",
			"script-torrent-done-filename":  "autorun_program",
			"script-torrent-added-filename": "autorun_on_torrent_added_program",
		}
		for field, pref := range strs {
			e.expectPrefs(field, with(form, field, "/fake/"+field), map[string]interface{}{pref: "/fake/" + field})
		}
		ints := map[string]string{
			"peer-limit-global":      "max_connec",
			"peer-limit-per-torrent": "max_connec_per_torrent",
			"download-queue-size":    "max_active_downloads",
			"seed-queue-size":        "max_active_uploads",
		}
		for field, pref := range ints {
			e.expectPrefs(field, with(form, field, n(field)+1), map[string]interface{}{pref: n(field) + 1})
		}

		// Version-gated name, checked against the dataset rather than Gates().
		addStopped := map[string]string{"4.6": "start_paused_enabled", "5.3": "add_stopped_enabled"}[e.ds.name]
		e.expectPrefs("start-added-torrents", with(form, "start-added-torrents", !b("start-added-torrents")),
			map[string]interface{}{addStopped: b("start-added-torrents")})

		e.expectPrefs("cache-size-mb", with(form, "cache-size-mb", 64), map[string]interface{}{"disk_cache": 64})
		e.expectPrefs("queue-stalled-minutes", with(form, "queue-stalled-minutes", 5),
			map[string]interface{}{"slow_torrent_inactive_timer": 300})
		e.expectPrefs("alt-speed-down", with(form, "alt-speed-down", 123), map[string]interface{}{"alt_dl_limit": 123 * 1024})
		e.expectPrefs("alt-speed-up", with(form, "alt-speed-up", 7), map[string]interface{}{"alt_up_limit": 7 * 1024})
		e.expectPrefs("alt-speed-time-begin", with(form, "alt-speed-time-begin", 8*60+30),
			map[string]interface{}{"schedule_from_hour": 8, "schedule_from_min": 30})
		e.expectPrefs("alt-speed-time-end", with(form, "alt-speed-time-end", 23*60+59),
			map[string]interface{}{"schedule_to_hour": 23, "schedule_to_min": 59})
		e.expectPrefs("encryption required", with(form, "encryption", "required"), map[string]interface{}{"encryption": 1})
		e.expectPrefs("encryption tolerated", with(form, "encryption", "tolerated"), map[string]interface{}{"encryption": 2})
		e.expectPrefs("encryption allowed", with(form, "encryption", "allowed"), map[string]interface{}{"encryption": 2})
		if b("utp-enabled") {
			e.expectPrefs("utp off", with(form, "utp-enabled", false), map[string]interface{}{"bittorrent_protocol": 1})
		} else {
			e.expectPrefs("utp on", with(form, "utp-enabled", true), map[string]interface{}{"bittorrent_protocol": 0})
		}
		e.expectPrefs("trash-original-torrent-files", with(form, "trash-original-torrent-files", !b("trash-original-torrent-files")),
			map[string]interface{}{"auto_delete_mode": map[bool]int{true: 0, false: 1}[b("trash-original-torrent-files")]})
		e.expectPrefs("default-trackers", with(form, "default-trackers", "udp://tracker.example:80/announce\n\nhttp://t2.example/announce"),
			map[string]interface{}{"add_trackers_enabled": true, "add_trackers": "udp://tracker.example:80/announce\n\nhttp://t2.example/announce"})

		e.expectPrefs("peer-port", with(form, "peer-port", n("peer-port")+1),
			map[string]interface{}{"random_port": false, "listen_port": n("peer-port") + 1})
		e.expectPrefs("peer-port-random-on-start", with(form, "peer-port-random-on-start", true),
			map[string]interface{}{"random_port": true})
		e.expectUnmappable("random port with a new port", with(form, "peer-port-random-on-start", true, "peer-port", 1234), "peer-port")

		// Shared queueing_enabled.
		q := b("download-queue-enabled")
		e.expectPrefs("both queues", with(form, "download-queue-enabled", !q, "seed-queue-enabled", !q),
			map[string]interface{}{"queueing_enabled": !q})
		e.expectUnmappable("download queue alone", with(form, "download-queue-enabled", !q), "download-queue-enabled")
		e.expectUnmappable("seed queue alone", with(form, "seed-queue-enabled", !q), "seed-queue-enabled")
	})
}

func TestSessionSetSpeedLimits(t *testing.T) {
	forEachSessionSet(t, nil, func(e *sessionSetEnv) {
		form := e.form()
		for _, dir := range []string{"down", "up"} {
			v, en := "speed-limit-"+dir, "speed-limit-"+dir+"-enabled"
			pref := map[string]string{"down": "dl_limit", "up": "up_limit"}[dir]
			if form[en].(bool) {
				cur := int(form[v].(float64))
				e.expectPrefs(v, with(form, v, cur+10), map[string]interface{}{pref: (cur + 10) * 1024})
				e.expectPrefs(en+" off", with(form, en, false), map[string]interface{}{pref: 0})
				// The new value can't be kept while off: dropped, the limit still goes off.
				e.expectPrefs(en+" off with a new value", with(form, en, false, v, cur+10), map[string]interface{}{pref: 0})
				e.expectUnmappable(v+" 0", with(form, v, 0), v)
			} else {
				e.expectPrefs(en+" on", with(form, en, true, v, 100), map[string]interface{}{pref: 100 * 1024})
				e.expectUnmappable(en+" on at 0", with(form, en, true, v, 0), en)
				e.expectNoWrite(v+" while off (dropped)", with(form, v, 100))
			}
		}
	})
}

func TestSessionSetShareLimits(t *testing.T) {
	forEachSessionSet(t, nil, func(e *sessionSetEnv) {
		form := e.form()
		if form["seedRatioLimited"].(bool) {
			e.expectPrefs("seedRatioLimit", with(form, "seedRatioLimit", 2.5),
				map[string]interface{}{"max_ratio_enabled": true, "max_ratio": 2.5})
			e.expectPrefs("seedRatioLimited off", with(form, "seedRatioLimited", false),
				map[string]interface{}{"max_ratio_enabled": false})
			e.expectPrefs("seedRatioLimited off with a new ratio", with(form, "seedRatioLimited", false, "seedRatioLimit", 3),
				map[string]interface{}{"max_ratio_enabled": false})
		} else {
			e.expectPrefs("seedRatioLimited on", with(form, "seedRatioLimited", true, "seedRatioLimit", 2.5),
				map[string]interface{}{"max_ratio_enabled": true, "max_ratio": 2.5})
			e.expectNoWrite("seedRatioLimit while off (dropped)", with(form, "seedRatioLimit", 2.5))
		}
		if form["idle-seeding-limit-enabled"].(bool) {
			e.t.Fatal("dataset has idle seeding on; test expects it off")
		}
		e.expectPrefs("idle-seeding-limit-enabled on", with(form, "idle-seeding-limit-enabled", true, "idle-seeding-limit", 30),
			map[string]interface{}{"max_inactive_seeding_time_enabled": true, "max_inactive_seeding_time": 30})
		e.expectNoWrite("idle-seeding-limit while off (dropped)", with(form, "idle-seeding-limit", 30))
	})
}

func TestSessionSetScheduleDays(t *testing.T) {
	forEachSessionSet(t, nil, func(e *sessionSetEnv) {
		form := e.form()
		if form["alt-speed-time-day"].(float64) != 127 {
			e.t.Fatalf("dataset alt-speed-time-day = %v, test expects 127", form["alt-speed-time-day"])
		}
		days := map[int]int{62: 1, 65: 2, 2: 3, 4: 4, 8: 5, 16: 6, 32: 7, 64: 8, 1: 9}
		for mask, want := range days {
			e.expectPrefs(fmt.Sprintf("days %d", mask), with(form, "alt-speed-time-day", mask),
				map[string]interface{}{"scheduler_days": want})
		}
		for _, mask := range []int{0, 3, 126, 63} {
			e.expectUnmappable(fmt.Sprintf("days %d", mask), with(form, "alt-speed-time-day", mask), "alt-speed-time-day")
		}
	})
}

func TestSessionSetUnmappable(t *testing.T) {
	forEachSessionSet(t, nil, func(e *sessionSetEnv) {
		form := e.form()
		dht := form["dht-enabled"].(bool)
		// A mappable change next to an unmappable one: nothing is written.
		e.expectUnmappable("blocklist-url", with(form, "dht-enabled", !dht, "blocklist-url", "http://list.example/x.gz"), "blocklist-url")
		e.expectUnmappable("several", with(form, "sequential_download", true, "script-torrent-done-seeding-enabled", true, "reqq", 500),
			"reqq", "script-torrent-done-seeding-enabled", "sequential_download")
		// Unchanged unmappable fields (TrguiNG always sends them) are fine.
		e.expectNoWrite("unchanged unmappable", with(form, "blocklist-url", "", "sequential_download", false))

		result, sent := e.set(with(form, "encryption", "bogus", "dht-enabled", !dht))
		if !strings.Contains(result, "encryption") || strings.Contains(result, "dht") || len(sent) != 0 {
			e.t.Errorf("invalid encryption: result %q, %d writes", result, len(sent))
		}
		result, sent = e.set(with(form, "peer-limit-global", "lots"))
		if !strings.Contains(result, "peer-limit-global") || len(sent) != 0 {
			e.t.Errorf("invalid number: result %q, %d writes", result, len(sent))
		}
	})
}

// A preference the qBT version doesn't report (here: as if older than 4.6) is
// unmappable rather than silently ignored.
func TestSessionSetMissingPreference(t *testing.T) {
	forEachSessionSet(t, func(p map[string]interface{}) {
		delete(p, "max_inactive_seeding_time")
		delete(p, "max_inactive_seeding_time_enabled")
	}, func(e *sessionSetEnv) {
		form := e.form()
		e.expectUnmappable("idle seeding", with(form, "idle-seeding-limit-enabled", true, "idle-seeding-limit", 30),
			"idle-seeding-limit")
		e.expectNoWrite("unchanged", form)
	})
}

// Both datasets are WebAPI >= 2.8.14, so alt speed goes through the absolute
// setSpeedLimitsMode; the toggle fallback is tested in qBT/session_test.go.
func TestSessionSetAltSpeed(t *testing.T) {
	forEachSessionSet(t, nil, func(e *sessionSetEnv) {
		form := e.form()
		if form["alt-speed-enabled"].(bool) {
			e.t.Fatal("dataset has alt speed on; test expects it off")
		}
		e.expectNoWrite("alt-speed-enabled", with(form, "alt-speed-enabled", true))
		// Changed together with a preference: one setPreferences plus the mode.
		e.expectPrefs("with alt-speed-down", with(form, "alt-speed-enabled", true, "alt-speed-down", 9),
			map[string]interface{}{"alt_dl_limit": 9 * 1024})
		sets := e.speedMode.all()
		if len(sets) != 2 || sets[0].Get("mode") != "1" || sets[1].Get("mode") != "1" {
			e.t.Errorf("setSpeedLimitsMode forms = %v, want two with mode=1", sets)
		}
		if e.toggle.count() != 0 {
			e.t.Errorf("toggleSpeedLimitsMode called %d times", e.toggle.count())
		}
	})
}

func TestStubs(t *testing.T) {
	forEachDataset(t, func(t *testing.T, ds *dataset, c *rpcClient) {
		if g := c.mustSucceed("group-get", nil)["group"]; !reflect.DeepEqual(g, []interface{}{}) {
			t.Errorf("group-get group = %#v", g)
		}
		if got := c.mustSucceed("group-set", map[string]interface{}{"name": "x", "speed-limit-down": 5}); len(got) != 0 {
			t.Errorf("group-set = %v", got)
		}
		if got := c.mustSucceed("blocklist-update", nil); !reflect.DeepEqual(got, map[string]interface{}{"blocklist-size": 0.0}) {
			t.Errorf("blocklist-update = %v", got)
		}
		wantOpen := ds.serverStateAfter(0)["connection_status"] == "connected"
		got := c.mustSucceed("port-test", nil)
		if !reflect.DeepEqual(got, map[string]interface{}{"port-is-open": wantOpen}) {
			t.Errorf("port-test = %v", got)
		}
		got = c.mustSucceed("port-test", map[string]interface{}{"ip_protocol": "ipv6"})
		if got["ip_protocol"] != "ipv6" {
			t.Errorf("port-test ipv6 = %v", got)
		}
		if out := c.call("port-test", map[string]interface{}{"ip_protocol": "ipx"}); out["result"] == "success" {
			t.Error("port-test with a bad ip_protocol succeeded")
		}
	})
}

func TestPortTestFirewalled(t *testing.T) {
	for _, status := range []string{"firewalled", "disconnected"} {
		t.Run(status, func(t *testing.T) {
			forEachDatasetWith(t, func(t *testing.T, ds *dataset) {
				initial := map[string]interface{}{}
				readJSONFile(t, filepath.Join(ds.dir, "sync_initial.json"), &initial)
				initial["server_state"].(map[string]interface{})["connection_status"] = status
				gock.New(testQBTAddr).Get("/api/v2/sync/maindata").MatchParam("rid", "^0$").Reply(200).JSON(initial)
			}, func(t *testing.T, ds *dataset, c *rpcClient) {
				if got := c.mustSucceed("port-test", nil); got["port-is-open"] != false {
					t.Errorf("port-test = %v", got)
				}
			})
		})
	}
}
