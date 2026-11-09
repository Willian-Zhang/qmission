package main

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/Willian-Zhang/qmission/qBT"
	log "github.com/sirupsen/logrus"
)

// session-set follows the diff rule (scrachpad/plan.md, Decisions): TrguiNG's
// settings dialog sends the whole session-get form back, so only fields that
// differ from a fresh session-get (readSession) count. Changed read-only fields
// are ignored, a changed field qBT can't store fails the request before
// anything is written, and the rest go out in one app/setPreferences call.

func init() { registerRPC("session-set", SessionSet) }

// sessionReadOnly are the session-get keys rpc-spec.md 4.1.1 excludes from
// session-set (old kebab names), plus download-dir-free-space, which has no
// setter in Transmission (rpcimpl.cc, setter nullptr).
var sessionReadOnly = stringSet(
	"blocklist-size", "config-dir", "download-dir-free-space", "rpc-version", "rpc-version-minimum",
	"rpc-version-semver", "session-id", "tcp-enabled", "units", "version",
)

// sessionUnmappable are writable Transmission session keys with no qBT
// preference. A change to any of them fails the request.
var sessionUnmappable = stringSet(
	"anti-brute-force-enabled", "anti-brute-force-threshold", // bridge RPC server, not qBT
	"blocklist-url",        // qBT's ip_filter_path is a local file, not a URL to fetch
	"preferred_transports", // newer than rpc 18; utp-enabled covers what qBT can do
	"reqq",
	"script-torrent-done-seeding-enabled", "script-torrent-done-seeding-filename",
	"sequential_download", // qBT has no "sequential by default" preference
)

func stringSet(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// sessionPlan collects what one session-set request turns into.
type sessionPlan struct {
	req     map[string]json.RawMessage
	current JsonMap
	changed map[string]bool
	keys    map[string]json.RawMessage // preference keys this qBT version reports
	gates   qBT.Gates

	prefs      map[string]interface{}
	owner      map[string]string // preference key -> Transmission field
	altSpeed   *bool
	unmappable []string
	invalid    []string
}

// value is the field's effective value: the request's if it changed, else the
// current one, both as plain JSON values (float64, bool, string, ...).
func (p *sessionPlan) value(field string) interface{} {
	if p.changed[field] {
		var v interface{}
		_ = json.Unmarshal(p.req[field], &v)
		return v
	}
	return normalizeJSON(p.current[field])
}

func (p *sessionPlan) bad(field string) {
	p.invalid = append(p.invalid, field)
}

func (p *sessionPlan) fail(fields ...string) {
	p.unmappable = append(p.unmappable, fields...)
}

// failChanged marks the changed ones among fields as unmappable.
func (p *sessionPlan) failChanged(fields ...string) {
	for _, f := range fields {
		if p.changed[f] {
			p.fail(f)
		}
	}
}

// drop records a changed value qBT can't keep because its limit is off (qBT
// stores "off" in the value itself). Like torrent-set, it's dropped, not an error.
func (p *sessionPlan) drop(field string) {
	log.WithField("field", field).Warn("session-set: value changed while its limit is off; qBittorrent can't keep it, dropped")
}

// limitOff handles a value/-enabled pair whose request says off: a changed value
// is dropped, and the off state is written only if the flag itself changed.
func (p *sessionPlan) limitOff(valueField, enabledField, pref string, off interface{}) {
	if p.changed[valueField] {
		p.drop(valueField)
	}
	if p.changed[enabledField] {
		p.set(enabledField, pref, off)
	}
}

func (p *sessionPlan) set(field, pref string, v interface{}) {
	p.prefs[pref] = v
	p.owner[pref] = field
}

func (p *sessionPlan) boolean(field string) (bool, bool) {
	v, ok := p.value(field).(bool)
	if !ok {
		p.bad(field)
	}
	return v, ok
}

func (p *sessionPlan) str(field string) (string, bool) {
	v, ok := p.value(field).(string)
	if !ok {
		p.bad(field)
	}
	return v, ok
}

// number accepts a JSON number or a numeric string (form inputs).
func (p *sessionPlan) number(field string, min, max float64) (float64, bool) {
	var f float64
	switch v := p.value(field).(type) {
	case float64:
		f = v
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			p.bad(field)
			return 0, false
		}
		f = parsed
	default:
		p.bad(field)
		return 0, false
	}
	if math.IsNaN(f) || f < min || f > max {
		p.bad(field)
		return 0, false
	}
	return f, true
}

func (p *sessionPlan) integer(field string, min, max int) (int, bool) {
	f, ok := p.number(field, float64(min), float64(max))
	if !ok {
		return 0, false
	}
	if f != math.Trunc(f) {
		p.bad(field)
		return 0, false
	}
	return int(f), true
}

// maxLimitKiB keeps limit*1024 inside qBT's int (setPreferences uses toInt).
const maxLimitKiB = math.MaxInt32 / 1024

// sessionGroup maps one or more Transmission fields that must be handled
// together (a value and its -enabled flag, say) to qBT preferences.
type sessionGroup struct {
	fields []string
	apply  func(p *sessionPlan)
}

func boolPref(field, pref string) sessionGroup {
	return sessionGroup{[]string{field}, func(p *sessionPlan) {
		if v, ok := p.boolean(field); ok {
			p.set(field, pref, v)
		}
	}}
}

func stringPref(field, pref string) sessionGroup {
	return sessionGroup{[]string{field}, func(p *sessionPlan) {
		if v, ok := p.str(field); ok {
			p.set(field, pref, v)
		}
	}}
}

func intPref(field, pref string, min, max int) sessionGroup {
	return sessionGroup{[]string{field}, func(p *sessionPlan) {
		if v, ok := p.integer(field, min, max); ok {
			p.set(field, pref, v)
		}
	}}
}

// speedLimitGroup: Transmission keeps a KiB/s value plus an -enabled flag; qBT
// has one B/s value with 0 = unlimited. So "off" is 0, a value changed while
// off is dropped, and an enabled limit of 0 can't be stored.
func speedLimitGroup(valueField, enabledField, pref string) sessionGroup {
	return sessionGroup{[]string{valueField, enabledField}, func(p *sessionPlan) {
		enabled, ok1 := p.boolean(enabledField)
		v, ok2 := p.integer(valueField, 0, maxLimitKiB)
		if !ok1 || !ok2 {
			return
		}
		if !enabled {
			p.limitOff(valueField, enabledField, pref, 0)
			return
		}
		if v == 0 {
			p.failChanged(valueField, enabledField)
			return
		}
		p.set(valueField, pref, v*1024)
	}}
}

func altLimitGroup(field, pref string) sessionGroup {
	return sessionGroup{[]string{field}, func(p *sessionPlan) {
		if v, ok := p.integer(field, 0, maxLimitKiB); ok {
			p.set(field, pref, v*1024)
		}
	}}
}

// scheduleTimeGroup: minutes after midnight -> hour + minute. qBT only reads
// the pair when both keys are present.
func scheduleTimeGroup(field, hourPref, minPref string) sessionGroup {
	return sessionGroup{[]string{field}, func(p *sessionPlan) {
		if v, ok := p.integer(field, 0, 24*60-1); ok {
			p.set(field, hourPref, v/60)
			p.set(field, minPref, v%60)
		}
	}}
}

// trSchedDaysToQBT inverts qBTSchedulerDaysToTR: Transmission's tr_sched_day
// bitmask (SUN 1 ... SAT 64) to qBT's Scheduler::Days. qBT has every day,
// weekdays, weekends and single days only.
func trSchedDaysToQBT(mask int) (int, bool) {
	switch mask {
	case 127:
		return 0, true
	case 62:
		return 1, true
	case 65:
		return 2, true
	case 1: // Sunday
		return 9, true
	}
	for day := 1; day <= 6; day++ { // Monday (2) .. Saturday (64)
		if mask == 1<<uint(day) {
			return day + 2, true
		}
	}
	return 0, false
}

// trEncryptionToQBT inverts qBTEncryptionToTR. qBT: 0 prefer encryption, 1
// require it, 2 disable it. Transmission's "tolerated" (4.1: "allowed") prefers
// clear text; qBT's nearest is "off", which session-get reports as tolerated.
func trEncryptionToQBT(mode string) (int, bool) {
	switch mode {
	case "preferred":
		return 0, true
	case "required":
		return 1, true
	case "tolerated", "allowed":
		return 2, true
	}
	return 0, false
}

var sessionGroups = []sessionGroup{
	speedLimitGroup("speed-limit-down", "speed-limit-down-enabled", "dl_limit"),
	speedLimitGroup("speed-limit-up", "speed-limit-up-enabled", "up_limit"),
	altLimitGroup("alt-speed-down", "alt_dl_limit"),
	altLimitGroup("alt-speed-up", "alt_up_limit"),
	{[]string{"alt-speed-enabled"}, func(p *sessionPlan) {
		if v, ok := p.boolean("alt-speed-enabled"); ok {
			p.altSpeed = &v
		}
	}},
	boolPref("alt-speed-time-enabled", "scheduler_enabled"),
	scheduleTimeGroup("alt-speed-time-begin", "schedule_from_hour", "schedule_from_min"),
	scheduleTimeGroup("alt-speed-time-end", "schedule_to_hour", "schedule_to_min"),
	{[]string{"alt-speed-time-day"}, func(p *sessionPlan) {
		mask, ok := p.integer("alt-speed-time-day", 0, 127)
		if !ok {
			return
		}
		if days, ok := trSchedDaysToQBT(mask); ok {
			p.set("alt-speed-time-day", "scheduler_days", days)
		} else {
			p.fail("alt-speed-time-day")
		}
	}},

	boolPref("blocklist-enabled", "ip_filter_enabled"),
	intPref("cache-size-mb", "disk_cache", -1, math.MaxInt32),
	{[]string{"default-trackers"}, func(p *sessionPlan) {
		v, ok := p.str("default-trackers")
		if !ok {
			return
		}
		// session-get shows add_trackers only while enabled, so "" means off;
		// the stored list is left alone then.
		if strings.TrimSpace(v) == "" {
			p.set("default-trackers", "add_trackers_enabled", false)
			return
		}
		p.set("default-trackers", "add_trackers_enabled", true)
		p.set("default-trackers", "add_trackers", v)
	}},
	boolPref("dht-enabled", "dht"),
	boolPref("lpd-enabled", "lsd"),
	boolPref("pex-enabled", "pex"),
	boolPref("port-forwarding-enabled", "upnp"),
	{[]string{"utp-enabled"}, func(p *sessionPlan) {
		// bittorrent_protocol: 0 TCP and uTP, 1 TCP only, 2 uTP only.
		if v, ok := p.boolean("utp-enabled"); ok {
			if v {
				p.set("utp-enabled", "bittorrent_protocol", 0)
			} else {
				p.set("utp-enabled", "bittorrent_protocol", 1)
			}
		}
	}},
	{[]string{"encryption"}, func(p *sessionPlan) {
		v, ok := p.str("encryption")
		if !ok {
			return
		}
		if enc, ok := trEncryptionToQBT(v); ok {
			p.set("encryption", "encryption", enc)
		} else {
			p.bad("encryption")
		}
	}},
	intPref("peer-limit-global", "max_connec", -1, math.MaxInt32),
	intPref("peer-limit-per-torrent", "max_connec_per_torrent", -1, math.MaxInt32),
	{[]string{"peer-port", "peer-port-random-on-start"}, func(p *sessionPlan) {
		random, ok1 := p.boolean("peer-port-random-on-start")
		port, ok2 := p.integer("peer-port", 0, 65535)
		if !ok1 || !ok2 {
			return
		}
		// qBT 4.6/5.x: random_port=true stores port 0 (a fresh port on every
		// start), so there is no fixed port to keep next to it.
		if random {
			if p.changed["peer-port"] {
				p.fail("peer-port")
				return
			}
			p.set("peer-port-random-on-start", "random_port", true)
			return
		}
		if port == 0 {
			p.failChanged("peer-port", "peer-port-random-on-start")
			return
		}
		owner := "peer-port"
		if !p.changed[owner] {
			owner = "peer-port-random-on-start"
		}
		p.set(owner, "random_port", false)
		p.set(owner, "listen_port", port)
	}},

	stringPref("download-dir", "save_path"),
	stringPref("incomplete-dir", "temp_path"),
	boolPref("incomplete-dir-enabled", "temp_path_enabled"),
	boolPref("rename-partial-files", "incomplete_files_ext"),
	{[]string{"start-added-torrents"}, func(p *sessionPlan) {
		if v, ok := p.boolean("start-added-torrents"); ok {
			p.set("start-added-torrents", p.gates.AddStoppedPref, !v)
		}
	}},
	{[]string{"trash-original-torrent-files"}, func(p *sessionPlan) {
		// auto_delete_mode: 0 never, 1 if added, 2 always.
		if v, ok := p.boolean("trash-original-torrent-files"); ok {
			if v {
				p.set("trash-original-torrent-files", "auto_delete_mode", 1)
			} else {
				p.set("trash-original-torrent-files", "auto_delete_mode", 0)
			}
		}
	}},

	{[]string{"download-queue-enabled", "seed-queue-enabled"}, func(p *sessionPlan) {
		// Both share qBT's queueing_enabled.
		dl, ok1 := p.boolean("download-queue-enabled")
		seed, ok2 := p.boolean("seed-queue-enabled")
		if !ok1 || !ok2 {
			return
		}
		if dl != seed {
			p.failChanged("download-queue-enabled", "seed-queue-enabled")
			return
		}
		owner := "download-queue-enabled"
		if !p.changed[owner] {
			owner = "seed-queue-enabled"
		}
		p.set(owner, "queueing_enabled", dl)
	}},
	intPref("download-queue-size", "max_active_downloads", -1, math.MaxInt32),
	intPref("seed-queue-size", "max_active_uploads", -1, math.MaxInt32),
	boolPref("queue-stalled-enabled", "dont_count_slow_torrents"),
	{[]string{"queue-stalled-minutes"}, func(p *sessionPlan) {
		if v, ok := p.integer("queue-stalled-minutes", 0, math.MaxInt32/60); ok {
			p.set("queue-stalled-minutes", "slow_torrent_inactive_timer", v*60)
		}
	}},

	{[]string{"seedRatioLimit", "seedRatioLimited"}, func(p *sessionPlan) {
		// 4.6.4 reads max_ratio only next to max_ratio_enabled (a missing one
		// becomes 0), 5.x either; so always send the pair.
		enabled, ok1 := p.boolean("seedRatioLimited")
		ratio, ok2 := p.number("seedRatioLimit", 0, math.MaxFloat32)
		if !ok1 || !ok2 {
			return
		}
		if !enabled {
			p.limitOff("seedRatioLimit", "seedRatioLimited", "max_ratio_enabled", false)
			return
		}
		owner := "seedRatioLimit"
		if !p.changed[owner] {
			owner = "seedRatioLimited"
		}
		p.set(owner, "max_ratio_enabled", true)
		p.set(owner, "max_ratio", ratio)
	}},
	{[]string{"idle-seeding-limit", "idle-seeding-limit-enabled"}, func(p *sessionPlan) {
		// Same pairing as the ratio (4.6.4 appcontroller.cpp:772).
		enabled, ok1 := p.boolean("idle-seeding-limit-enabled")
		minutes, ok2 := p.integer("idle-seeding-limit", 0, math.MaxInt32)
		if !ok1 || !ok2 {
			return
		}
		if !enabled {
			p.limitOff("idle-seeding-limit", "idle-seeding-limit-enabled", "max_inactive_seeding_time_enabled", false)
			return
		}
		owner := "idle-seeding-limit"
		if !p.changed[owner] {
			owner = "idle-seeding-limit-enabled"
		}
		p.set(owner, "max_inactive_seeding_time_enabled", true)
		p.set(owner, "max_inactive_seeding_time", minutes)
	}},

	boolPref("script-torrent-done-enabled", "autorun_enabled"),
	stringPref("script-torrent-done-filename", "autorun_program"),
	boolPref("script-torrent-added-enabled", "autorun_on_torrent_added_enabled"),
	stringPref("script-torrent-added-filename", "autorun_on_torrent_added_program"),
}

// sessionGroupOf maps each writable mapped field to its group.
var sessionGroupOf = func() map[string]int {
	m := map[string]int{}
	for i, g := range sessionGroups {
		for _, f := range g.fields {
			m[f] = i
		}
	}
	return m
}()

func isSessionKey(key string) bool {
	_, mapped := sessionGroupOf[key]
	return mapped || sessionReadOnly[key] || sessionUnmappable[key]
}

func SessionSet(args json.RawMessage) (JsonMap, error) {
	var req map[string]json.RawMessage
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, fmt.Errorf("session-set: invalid arguments: %w", err)
	}
	for key := range req {
		if !isSessionKey(key) {
			logFieldOnce("session-set:"+key, "Unknown session-set field, ignored")
		}
	}

	current, keys, err := readSession()
	if err != nil {
		return nil, err
	}
	p := &sessionPlan{
		req:     req,
		current: current,
		changed: map[string]bool{},
		keys:    keys,
		gates:   qBTConn.Gates(),
		prefs:   map[string]interface{}{},
		owner:   map[string]string{},
	}

	var readOnly []string
	for _, key := range changedKeys(req, []JsonMap{current}, isSessionKey) {
		switch {
		case sessionReadOnly[key]:
			readOnly = append(readOnly, key)
		case sessionUnmappable[key]:
			p.fail(key)
		default:
			p.changed[key] = true
		}
	}
	if len(readOnly) > 0 {
		log.WithField("fields", strings.Join(readOnly, ",")).Debug("session-set: ignoring changed read-only fields")
	}

	done := map[int]bool{}
	for _, key := range sortedKeys(p.changed) {
		i := sessionGroupOf[key]
		if !done[i] {
			done[i] = true
			sessionGroups[i].apply(p)
		}
	}

	// A preference this qBT version doesn't report would be ignored silently.
	for pref, field := range p.owner {
		if _, ok := p.keys[pref]; !ok {
			p.fail(field)
		}
	}

	if len(p.unmappable) > 0 {
		fields := dedupe(p.unmappable)
		log.WithField("fields", strings.Join(fields, ",")).Warn("session-set: changed fields qBittorrent can't store; nothing applied")
		return nil, unmappableError(fields)
	}
	if len(p.invalid) > 0 {
		return nil, fmt.Errorf("session-set: invalid value for %s, nothing was changed", strings.Join(dedupe(p.invalid), ", "))
	}

	if len(p.prefs) > 0 {
		names := make([]string, 0, len(p.prefs))
		for pref := range p.prefs {
			names = append(names, pref)
		}
		sort.Strings(names)
		log.WithField("prefs", strings.Join(names, ",")).Debug("session-set: app/setPreferences")
		if err := qBTConn.SetPreferences(p.prefs); err != nil {
			return nil, err
		}
	}
	if p.altSpeed != nil {
		if err := qBTConn.SetAltSpeedLimits(*p.altSpeed); err != nil {
			return nil, err
		}
	}
	return JsonMap{}, nil
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func dedupe(list []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range list {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
