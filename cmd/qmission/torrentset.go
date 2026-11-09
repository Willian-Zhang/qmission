package main

// torrent-set with the diff rule (scrachpad/plan.md, Decisions).
//
// Keys come in two kinds:
//   - state keys (limits, ratio, labels, trackerList, ...) are compared with the
//     torrents' current values in Transmission terms, read fresh for this request
//     (parseIDsField refreshes sync/maindata; torrents/properties, torrents/trackers
//     and torrents/files are read only when a key needs them). A key is changed
//     only if it differs from every targeted torrent; unchanged keys are skipped.
//   - command keys (files-wanted/-unwanted, priority-*, trackerAdd/Remove/Replace)
//     aren't state and are always applied.
//
// Everything is planned (and validated) before the first write, so a changed
// field qBT can't store fails the request with nothing applied. Writes then run
// in order and stop at the first qBT error; earlier writes stay applied.

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/Willian-Zhang/qmission/qBT"
	"github.com/Willian-Zhang/qmission/transmission"
	log "github.com/sirupsen/logrus"
)

func init() { registerRPC("torrent-set", TorrentSetDiff) }

// Transmission tr_ratiolimit / tr_idlelimit (transmission.h).
const (
	trModeGlobal    = 0
	trModeSingle    = 1
	trModeUnlimited = 2
)

// qBT share-limit sentinels (torrent.h / sharelimits.h).
const (
	qBTLimitGlobal    = -2
	qBTLimitUnlimited = -1
)

// torrent-set state keys from rpc-spec.md 3.2 (classic names, rpc 18), by type.
var (
	torrentSetBoolKeys = map[string]bool{
		"downloadLimited": true, "uploadLimited": true, "honorsSessionLimits": true, "sequential_download": true,
	}
	torrentSetNumberKeys = map[string]bool{
		"bandwidthPriority": true, "downloadLimit": true, "uploadLimit": true, "peer-limit": true,
		"queuePosition": true, "seedIdleLimit": true, "seedIdleMode": true, "seedRatioLimit": true,
		"seedRatioMode": true, "sequential_download_from_piece": true,
	}
	torrentSetStringKeys = map[string]bool{"group": true, "location": true, "trackerList": true}
	torrentSetFileKeys   = []string{"files-unwanted", "files-wanted", "priority-high", "priority-low", "priority-normal"}
)

// torrentSetUnmappable are state keys qBT has no per-torrent setting for.
var torrentSetUnmappable = map[string]string{
	"bandwidthPriority":              "qBittorrent has no per-torrent bandwidth priority",
	"group":                          "qBittorrent has no bandwidth groups",
	"honorsSessionLimits":            "qBittorrent torrents always honor the global limits",
	"peer-limit":                     "the WebAPI can't set a per-torrent connection limit",
	"sequential_download_from_piece": "qBittorrent can't start sequential download at a piece",
}

func isTorrentSetStateKey(key string) bool {
	return torrentSetBoolKeys[key] || torrentSetNumberKeys[key] || torrentSetStringKeys[key] || key == "labels"
}

func isTorrentSetCommandKey(key string) bool {
	switch key {
	case "files-unwanted", "files-wanted", "priority-high", "priority-low", "priority-normal",
		"trackerAdd", "trackerRemove", "trackerReplace":
		return true
	}
	return false
}

// --- request parsing

func parseJSONBool(raw json.RawMessage) (bool, error) {
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b, nil
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err == nil && (n == 0 || n == 1) {
		return n == 1, nil
	}
	return false, fmt.Errorf("want a boolean, got %s", raw)
}

// parseJSONNumber also takes numeric strings: form inputs (Mantine's
// NumberInput) can hand them over.
func parseJSONNumber(raw json.RawMessage) (float64, error) {
	var n float64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if n, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil && !math.IsInf(n, 0) && !math.IsNaN(n) {
			return n, nil
		}
	}
	return 0, fmt.Errorf("want a number, got %s", raw)
}

// canonicalLabels trims, drops empty labels and sorts/dedupes, so the
// comparison with qBT's tags doesn't depend on order.
func canonicalLabels(labels []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		l = strings.TrimSpace(l)
		if l == "" || seen[l] {
			continue
		}
		if strings.Contains(l, ",") {
			return nil, fmt.Errorf("label %q contains a comma, which qBittorrent tags can't hold", l)
		}
		if isAddMarker(l) {
			return nil, fmt.Errorf("labels starting with %q are reserved by qmission", addMarkerPrefix)
		}
		seen[l] = true
		out = append(out, l)
	}
	sort.Strings(out)
	return out, nil
}

// parseTrackerTiers splits a trackerList into tiers like qBT and Transmission
// do: one URL per line, blank lines between tiers. Lines are trimmed (qBT's
// parseTrackerEntries does too), empty tiers dropped, repeated URLs kept once.
func parseTrackerTiers(list string) [][]string {
	var tiers [][]string
	var cur []string
	seen := map[string]bool{}
	for _, line := range strings.Split(list, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			if len(cur) > 0 {
				tiers = append(tiers, cur)
				cur = nil
			}
			continue
		}
		if !seen[line] {
			seen[line] = true
			cur = append(cur, line)
		}
	}
	if len(cur) > 0 {
		tiers = append(tiers, cur)
	}
	return tiers
}

func joinTrackerTiers(tiers [][]string) string {
	parts := make([]string, len(tiers))
	for i, urls := range tiers {
		parts[i] = strings.Join(urls, "\n")
	}
	return strings.Join(parts, "\n\n")
}

func canonicalTrackerList(list string) string { return joinTrackerTiers(parseTrackerTiers(list)) }

// canonicalStateValue parses one state key into the plain JSON type its
// current value normalizes to (bool, float64, string, sorted []string).
func canonicalStateValue(key string, raw json.RawMessage) (interface{}, error) {
	switch {
	case torrentSetBoolKeys[key]:
		return parseJSONBool(raw)
	case torrentSetNumberKeys[key]:
		return parseJSONNumber(raw)
	case key == "labels":
		var labels []string
		if err := json.Unmarshal(raw, &labels); err != nil {
			return nil, fmt.Errorf("want an array of strings, got %s", raw)
		}
		return canonicalLabels(labels)
	case key == "trackerList":
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("want a string, got %s", raw)
		}
		return canonicalTrackerList(s), nil
	default:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("want a string, got %s", raw)
		}
		return s, nil
	}
}

func parseIndexList(raw json.RawMessage) ([]int, error) {
	var nums []float64
	if err := json.Unmarshal(raw, &nums); err != nil {
		return nil, fmt.Errorf("want an array of file indices, got %s", raw)
	}
	out := make([]int, len(nums))
	for i, n := range nums {
		if n != math.Trunc(n) || n < 0 || n > math.MaxInt32 {
			return nil, fmt.Errorf("invalid file index %v", n)
		}
		out[i] = int(n)
	}
	return out, nil
}

// torrentSetCommands are the keys that are applied whenever present.
type torrentSetCommands struct {
	files          map[string][]int // file key -> indices; an empty list means all files
	trackerAdd     []string
	trackerRemove  []int
	trackerReplace []trackerReplacement
}

type trackerReplacement struct {
	id  int
	url string
}

func (c torrentSetCommands) hasTrackerCommands() bool {
	return c.trackerAdd != nil || c.trackerRemove != nil || c.trackerReplace != nil
}

func parseTorrentSetCommands(req map[string]json.RawMessage) (torrentSetCommands, error) {
	cmds := torrentSetCommands{files: map[string][]int{}}
	for _, key := range torrentSetFileKeys {
		if raw, ok := req[key]; ok {
			ids, err := parseIndexList(raw)
			if err != nil {
				return cmds, fmt.Errorf("torrent-set: invalid %s: %w", key, err)
			}
			cmds.files[key] = ids
		}
	}
	if raw, ok := req["trackerAdd"]; ok {
		if err := json.Unmarshal(raw, &cmds.trackerAdd); err != nil {
			return cmds, fmt.Errorf("torrent-set: invalid trackerAdd: want an array of URLs, got %s", raw)
		}
		if cmds.trackerAdd == nil {
			cmds.trackerAdd = []string{}
		}
	}
	if raw, ok := req["trackerRemove"]; ok {
		ids, err := parseIndexList(raw)
		if err != nil {
			return cmds, fmt.Errorf("torrent-set: invalid trackerRemove: %w", err)
		}
		cmds.trackerRemove = ids
	}
	if raw, ok := req["trackerReplace"]; ok {
		var pairs []interface{}
		if err := json.Unmarshal(raw, &pairs); err != nil || len(pairs)%2 != 0 {
			return cmds, fmt.Errorf("torrent-set: invalid trackerReplace: want [id, url, ...], got %s", raw)
		}
		cmds.trackerReplace = []trackerReplacement{}
		for i := 0; i < len(pairs); i += 2 {
			id, okID := pairs[i].(float64)
			u, okURL := pairs[i+1].(string)
			if !okID || !okURL || id != math.Trunc(id) || id < 0 {
				return cmds, fmt.Errorf("torrent-set: invalid trackerReplace pair %v, %v", pairs[i], pairs[i+1])
			}
			cmds.trackerReplace = append(cmds.trackerReplace, trackerReplacement{int(id), strings.TrimSpace(u)})
		}
	}
	return cmds, nil
}

// --- current values

// tsTracker is one real tracker as torrent-get maps it (id = position).
type tsTracker struct {
	id       int
	announce string
	tier     int
}

type tsTorrent struct {
	t        *qBT.TorrentInfo
	cur      JsonMap // current values in Transmission terms (what torrent-get answers)
	trackers []tsTracker
}

// readTorrentSetCurrents builds each torrent's current values like torrent-get
// does (list mapping, template values for unmapped fields), reading the
// per-torrent endpoints only for keys present in the request.
func readTorrentSetCurrents(torrents qBT.TorrentInfoList, keys map[string]bool) ([]tsTorrent, error) {
	needTrackers := keys["trackerList"] || keys["trackerAdd"] || keys["trackerRemove"] || keys["trackerReplace"]
	var positions map[qBT.Hash]int
	if keys["queuePosition"] {
		positions = queuePositions(qBTConn.TorrentsList.Slice())
	}
	defaults := transmission.TorrentGetDefaults()
	items := make([]tsTorrent, 0, len(torrents))
	for _, t := range torrents {
		translated := JsonMap{}
		MapTorrentList(translated, t)
		cur := JsonMap{}
		for key := range keys {
			if !isTorrentSetStateKey(key) {
				continue
			}
			if v, ok := translated[key]; ok {
				cur[key] = v
			} else if v, ok := defaults[key]; ok {
				cur[key] = v
			}
		}
		if keys["labels"] {
			cur["labels"], _ = canonicalLabels(parseTags(t.Tags))
		}
		if keys["location"] {
			cur["location"] = t.Save_path
		}
		if positions != nil {
			cur["queuePosition"] = positions[t.Hash]
		}
		// torrent-get reads peer-limit from torrents/properties, except while
		// fetching metadata (planDetails), where it answers the template.
		if keys["peer-limit"] && !isFetchingMetadata(t.State) {
			props, err := qBTConn.GetPropsGeneral(t.Hash)
			if err != nil {
				return nil, err
			}
			cur["peer-limit"] = props.Nb_connections_limit
		}
		item := tsTorrent{t: t, cur: cur}
		if needTrackers {
			list, err := qBTConn.GetPropsTrackers(t.Hash)
			if err != nil {
				return nil, err
			}
			mapped := JsonMap{}
			MapPropsTrackers(mapped, list)
			for _, tr := range mapped["trackers"].([]JsonMap) {
				item.trackers = append(item.trackers, tsTracker{id: tr["id"].(int), announce: tr["announce"].(string), tier: tr["tier"].(int)})
			}
			cur["trackerList"] = canonicalTrackerList(mapped["trackerList"].(string))
		}
		items = append(items, item)
	}
	return items, nil
}

// --- planning

type tsStep struct {
	what string
	run  func() error
}

// orderedBatch groups hashes by a value, keeping first-seen order, so one qBT
// call covers every torrent that gets the same value.
type orderedBatch[K comparable] struct {
	order  []K
	hashes map[K][]qBT.Hash
}

func newOrderedBatch[K comparable]() *orderedBatch[K] {
	return &orderedBatch[K]{hashes: map[K][]qBT.Hash{}}
}

func (b *orderedBatch[K]) add(k K, h qBT.Hash) {
	if _, ok := b.hashes[k]; !ok {
		b.order = append(b.order, k)
	}
	b.hashes[k] = append(b.hashes[k], h)
}

type torrentSetPlan struct {
	items      []tsTorrent
	state      map[string]interface{} // canonical request values
	changed    map[string]bool
	cmds       torrentSetCommands
	unmappable map[string]string // field -> reason
	steps      []tsStep
}

func (p *torrentSetPlan) step(what string, run func() error) {
	p.steps = append(p.steps, tsStep{what, run})
}

// eff is the value a torrent should end up with for key: the request's if the
// key changed, else the torrent's own current value.
func (p *torrentSetPlan) eff(key string, i int) interface{} {
	if p.changed[key] {
		return p.state[key]
	}
	return normalizeJSON(p.items[i].cur[key])
}

func (p *torrentSetPlan) effNum(key string, i int) float64 {
	v, _ := p.eff(key, i).(float64)
	return v
}

func (p *torrentSetPlan) effBool(key string, i int) bool {
	v, _ := p.eff(key, i).(bool)
	return v
}

func (p *torrentSetPlan) curNum(key string, i int) float64 {
	v, _ := normalizeJSON(p.items[i].cur[key]).(float64)
	return v
}

func (p *torrentSetPlan) curBool(key string, i int) bool {
	v, _ := normalizeJSON(p.items[i].cur[key]).(bool)
	return v
}

func (p *torrentSetPlan) hashes() []qBT.Hash {
	h := make([]qBT.Hash, len(p.items))
	for i, it := range p.items {
		h[i] = it.t.Hash
	}
	return h
}

func jsonNumberOr(n json.Number, def int64) int64 {
	if v, err := n.Int64(); err == nil {
		return v
	}
	return def
}

func (p *torrentSetPlan) planSpeedLimit(limitKey, limitedKey string, set func([]qBT.Hash, int64) error, endpoint string) error {
	if !p.changed[limitKey] && !p.changed[limitedKey] {
		return nil
	}
	batch := newOrderedBatch[int64]()
	skipped := 0
	for i, it := range p.items {
		limited, kib := p.effBool(limitedKey, i), p.effNum(limitKey, i)
		curLimited, curKiB := p.curBool(limitedKey, i), p.curNum(limitKey, i)
		if limited == curLimited && (!limited || kib == curKiB) {
			if !limited && kib != curKiB {
				skipped++
			}
			continue
		}
		var bytes int64 // 0 = unlimited (qBT turns 0 into -1 and stores no limit)
		if limited {
			if kib < 1 {
				return fmt.Errorf("torrent-set: %s %v can't be stored: qBittorrent treats 0 as unlimited (use %s=false)", limitKey, kib, limitedKey)
			}
			bytes = int64(math.Round(kib * 1024))
		}
		batch.add(bytes, it.t.Hash)
	}
	if skipped > 0 {
		log.WithField("field", limitKey).WithField("torrents", skipped).
			Warn("torrent-set: limit not stored for torrents with the limit off (qBittorrent keeps no limit value while it's off)")
	}
	for _, v := range batch.order {
		v, hashes := v, batch.hashes[v]
		p.step(endpoint, func() error { return set(hashes, v) })
	}
	return nil
}

// planShareLimits: ratio and idle limits go through one setShareLimits call,
// which needs every limit, so the parts that didn't change keep each torrent's
// current qBT value.
func (p *torrentSetPlan) planShareLimits() error {
	ratioChanged := p.changed["seedRatioMode"] || p.changed["seedRatioLimit"]
	idleChanged := p.changed["seedIdleMode"] || p.changed["seedIdleLimit"]
	if !ratioChanged && !idleChanged {
		return nil
	}
	if idleChanged && !qBTConn.APIVersion().AtLeast(qBT.InactiveSeedingAPIVersion) {
		for _, key := range []string{"seedIdleMode", "seedIdleLimit"} {
			if p.changed[key] {
				p.unmappable[key] = "qBittorrent before 4.6 (WebAPI 2.9.2) has no inactive seeding limit"
			}
		}
		return nil
	}
	batch := newOrderedBatch[qBT.ShareLimits]()
	for i, it := range p.items {
		t := it.t
		curIdle := jsonNumberOr(t.Inactive_seeding_time_limit, qBTLimitGlobal)
		limits := qBT.ShareLimits{
			RatioLimit:               t.Ratio_limit,
			SeedingTimeLimit:         jsonNumberOr(t.Seeding_time_limit, qBTLimitGlobal),
			InactiveSeedingTimeLimit: curIdle,
			Action:                   t.Share_limit_action,
			Mode:                     t.Share_limits_mode,
		}
		if ratioChanged {
			switch mode, limit := p.effNum("seedRatioMode", i), p.effNum("seedRatioLimit", i); mode {
			case trModeGlobal:
				limits.RatioLimit = qBTLimitGlobal
			case trModeSingle:
				if limit < 0 {
					return fmt.Errorf("torrent-set: invalid seedRatioLimit %v", limit)
				}
				limits.RatioLimit = limit
			case trModeUnlimited:
				limits.RatioLimit = qBTLimitUnlimited
			default:
				return fmt.Errorf("torrent-set: invalid seedRatioMode %v", mode)
			}
		}
		if idleChanged {
			switch mode, limit := p.effNum("seedIdleMode", i), p.effNum("seedIdleLimit", i); mode {
			case trModeGlobal:
				limits.InactiveSeedingTimeLimit = qBTLimitGlobal
			case trModeSingle:
				if limit < 0 || limit != math.Trunc(limit) {
					return fmt.Errorf("torrent-set: invalid seedIdleLimit %v", limit)
				}
				limits.InactiveSeedingTimeLimit = int64(limit)
			case trModeUnlimited:
				limits.InactiveSeedingTimeLimit = qBTLimitUnlimited
			default:
				return fmt.Errorf("torrent-set: invalid seedIdleMode %v", mode)
			}
		}
		if limits.RatioLimit == t.Ratio_limit && limits.InactiveSeedingTimeLimit == curIdle {
			continue
		}
		batch.add(limits, t.Hash)
	}
	for _, l := range batch.order {
		l, hashes := l, batch.hashes[l]
		p.step("torrents/setShareLimits", func() error { return qBTConn.SetTorrentsShareLimits(hashes, l) })
	}
	return nil
}

// planSequential keeps qBT's first/last-piece priority in step with sequential
// download, as Transmission's sequential mode includes it. Both are toggles,
// so only torrents whose current flag differs are sent.
func (p *torrentSetPlan) planSequential() {
	if !p.changed["sequential_download"] {
		return
	}
	target := p.state["sequential_download"].(bool)
	var seq, firstLast []qBT.Hash
	for _, it := range p.items {
		if it.t.Seq_dl != target {
			seq = append(seq, it.t.Hash)
		}
		if it.t.F_l_piece_prio != target {
			firstLast = append(firstLast, it.t.Hash)
		}
	}
	if len(seq) > 0 {
		p.step("torrents/toggleSequentialDownload", func() error { return qBTConn.ToggleSequential(seq) })
	}
	if len(firstLast) > 0 {
		p.step("torrents/toggleFirstLastPiecePrio", func() error { return qBTConn.ToggleFirstLastPiece(firstLast) })
	}
}

// planLabels diffs the wanted labels against each torrent's tags. parseTags
// leaves out torrent-add's marker tags, so they're never removed here.
func (p *torrentSetPlan) planLabels() {
	if !p.changed["labels"] {
		return
	}
	want := p.state["labels"].([]string)
	wantSet := map[string]bool{}
	for _, l := range want {
		wantSet[l] = true
	}
	adds, removes := newOrderedBatch[string](), newOrderedBatch[string]()
	for _, it := range p.items {
		have := map[string]bool{}
		var remove []string
		for _, tag := range parseTags(it.t.Tags) {
			have[tag] = true
			if !wantSet[tag] {
				remove = append(remove, tag)
			}
		}
		var add []string
		for _, l := range want {
			if !have[l] {
				add = append(add, l)
			}
		}
		if len(add) > 0 {
			adds.add(strings.Join(add, ","), it.t.Hash)
		}
		if len(remove) > 0 {
			sort.Strings(remove)
			removes.add(strings.Join(remove, ","), it.t.Hash)
		}
	}
	for _, tags := range removes.order {
		tags, hashes := tags, removes.hashes[tags]
		p.step("torrents/removeTags", func() error { return qBTConn.RemoveTags(hashes, strings.Split(tags, ",")) })
	}
	for _, tags := range adds.order {
		tags, hashes := tags, adds.hashes[tags]
		p.step("torrents/addTags", func() error { return qBTConn.AddTorrentTags(hashes, strings.Split(tags, ",")) })
	}
}

func (p *torrentSetPlan) planLocation() error {
	if !p.changed["location"] {
		return nil
	}
	location := strings.TrimSpace(p.state["location"].(string))
	if location == "" {
		return errors.New("torrent-set: location can't be empty")
	}
	var hashes []qBT.Hash
	for _, it := range p.items {
		if it.t.Save_path != location {
			hashes = append(hashes, it.t.Hash)
		}
	}
	if len(hashes) > 0 {
		p.step("torrents/setLocation", func() error { return qBTConn.SetLocation(hashes, location) })
	}
	return nil
}

// planQueuePosition moves one queued torrent to a Transmission queue position.
// queuePositions puts qBT's queued torrents (priority 1..Q) first, so position
// p is qBT priority p+1; qBT can only move torrents that are in its queue.
func (p *torrentSetPlan) planQueuePosition() error {
	if !p.changed["queuePosition"] {
		return nil
	}
	if len(p.items) != 1 {
		return errors.New("torrent-set: queuePosition needs exactly one torrent (use queue-move-* for several)")
	}
	pos := p.state["queuePosition"].(float64)
	if pos < 0 || pos != math.Trunc(pos) {
		return fmt.Errorf("torrent-set: invalid queuePosition %v", pos)
	}
	t := p.items[0].t
	if t.Priority <= 0 {
		return errors.New("torrent-set: queuePosition can't be set: the torrent isn't in qBittorrent's queue (queueing disabled, or it's seeding or forced)")
	}
	queued := 0
	for _, other := range qBTConn.TorrentsList.Slice() {
		if other.Priority > 0 {
			queued++
		}
	}
	target := int(pos) + 1
	if target > queued {
		target = queued
	}
	hashes := []qBT.Hash{t.Hash}
	switch {
	case target == t.Priority:
	case target == 1:
		p.step(string(qBT.QueueTop), func() error { return qBTConn.MoveInQueue(qBT.QueueTop, hashes) })
	case target == queued:
		p.step(string(qBT.QueueBottom), func() error { return qBTConn.MoveInQueue(qBT.QueueBottom, hashes) })
	default:
		move, n := qBT.QueueUp, t.Priority-target
		if n < 0 {
			move, n = qBT.QueueDown, -n
		}
		for k := 0; k < n; k++ {
			p.step(string(move), func() error { return qBTConn.MoveInQueue(move, hashes) })
		}
	}
	return nil
}

// trackerURLsString renders (url, tier) pairs for torrents/addTrackers: each
// blank line moves qBT's parser to the next tier, starting at 0.
func trackerURLsString(urls []string, tiers []int) string {
	idx := make([]int, len(urls))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return tiers[idx[a]] < tiers[idx[b]] })
	var lines []string
	tier := 0
	for _, i := range idx {
		for ; tier < tiers[i]; tier++ {
			lines = append(lines, "")
		}
		lines = append(lines, urls[i])
	}
	return strings.Join(lines, "\n")
}

// removableTrackerURL: torrents/removeTrackers splits on "|", and qBT 5.0.5+
// also percent-decodes each URL (5.0.0 doesn't; both are WebAPI 2.11.2), so
// URLs with "|" or "%" can't be removed reliably.
func removableTrackerURL(u string) bool { return !strings.ContainsAny(u, "|%") }

// planTrackerList turns a trackerList into per-torrent removeTrackers/
// addTrackers calls. qBT keeps each existing tracker's tier, so only URLs are
// added and removed; new URLs get the tier of a kept URL in the same group, or
// the next tier after the previous group. If the result wouldn't have the
// requested tier layout (tiers reordered, or a new tier squeezed between two
// adjacent ones), the field is unmappable.
func (p *torrentSetPlan) planTrackerList() {
	if !p.changed["trackerList"] {
		return
	}
	wantTiers := parseTrackerTiers(p.state["trackerList"].(string))
	want := joinTrackerTiers(wantTiers)
	for _, it := range p.items {
		if it.cur["trackerList"] == want {
			continue
		}
		curTier := map[string]int{}
		for _, tr := range it.trackers {
			curTier[tr.announce] = tr.tier
		}
		wanted := map[string]bool{}
		var newURLs []string
		var newTiers []int
		final := map[string]int{} // url -> resulting qBT tier
		prev := -1
		for _, group := range wantTiers {
			groupTier := -1
			for _, u := range group {
				wanted[u] = true
				if t, ok := curTier[u]; ok && (groupTier < 0 || t < groupTier) {
					groupTier = t
				}
			}
			if groupTier < 0 {
				groupTier = prev + 1
			}
			for _, u := range group {
				if t, ok := curTier[u]; ok {
					final[u] = t
				} else {
					final[u] = groupTier
					newURLs = append(newURLs, u)
					newTiers = append(newTiers, groupTier)
				}
				if final[u] > prev {
					prev = final[u]
				}
			}
		}
		var remove []string
		for _, tr := range it.trackers {
			if !wanted[tr.announce] {
				if !removableTrackerURL(tr.announce) {
					p.unmappable["trackerList"] = fmt.Sprintf("torrent %d: a tracker URL containing | or %% can't be removed through the WebAPI", it.t.Id)
					return
				}
				remove = append(remove, tr.announce)
			}
		}
		// Predict qBT's resulting layout (grouped by tier) and compare.
		var predicted []tsTracker
		for u, t := range final {
			predicted = append(predicted, tsTracker{announce: u, tier: t})
		}
		order := map[string]int{}
		n := 0
		for _, group := range wantTiers {
			for _, u := range group {
				order[u] = n
				n++
			}
		}
		sort.Slice(predicted, func(a, b int) bool {
			if predicted[a].tier != predicted[b].tier {
				return predicted[a].tier < predicted[b].tier
			}
			return order[predicted[a].announce] < order[predicted[b].announce]
		})
		var predTiers [][]string
		for k, tr := range predicted {
			if k == 0 || predicted[k-1].tier != tr.tier {
				predTiers = append(predTiers, nil)
			}
			predTiers[len(predTiers)-1] = append(predTiers[len(predTiers)-1], tr.announce)
		}
		if joinTrackerTiers(predTiers) != want {
			p.unmappable["trackerList"] = fmt.Sprintf("torrent %d: qBittorrent can't move existing trackers between tiers or insert a tier between adjacent ones (add new tiers at the end)", it.t.Id)
			return
		}
		hash := it.t.Hash
		if len(remove) > 0 {
			p.step("torrents/removeTrackers", func() error { return qBTConn.RemoveTorrentTrackers(hash, remove) })
		}
		if len(newURLs) > 0 {
			urls := trackerURLsString(newURLs, newTiers)
			p.step("torrents/addTrackers", func() error { return qBTConn.AddTorrentTrackers(hash, urls) })
		}
	}
}

// planTrackerCommands maps the deprecated trackerAdd/Remove/Replace. IDs are
// torrent-get's tracker ids (position in tier order). trackerAdd puts each new
// URL in a new tier after the last one, like Transmission (announce_list::add).
func (p *torrentSetPlan) planTrackerCommands() error {
	c := p.cmds
	if !c.hasTrackerCommands() {
		return nil
	}
	if p.changed["trackerList"] {
		return errors.New("torrent-set: send either trackerList or trackerAdd/trackerRemove/trackerReplace, not both")
	}
	for _, it := range p.items {
		hash := it.t.Hash
		byID := map[int]string{}
		have := map[string]bool{}
		maxTier := -1
		for _, tr := range it.trackers {
			byID[tr.id] = tr.announce
			have[tr.announce] = true
			if tr.tier > maxTier {
				maxTier = tr.tier
			}
		}
		var add []string
		var tiers []int
		for _, u := range c.trackerAdd {
			if u = strings.TrimSpace(u); u != "" && !have[u] {
				have[u] = true
				maxTier++
				add = append(add, u)
				tiers = append(tiers, maxTier)
			}
		}
		if len(add) > 0 {
			urls := trackerURLsString(add, tiers)
			p.step("torrents/addTrackers", func() error { return qBTConn.AddTorrentTrackers(hash, urls) })
		}
		var remove []string
		for _, id := range c.trackerRemove {
			u, ok := byID[id]
			if !ok {
				return fmt.Errorf("torrent-set: trackerRemove: torrent %d has no tracker id %d", it.t.Id, id)
			}
			if !removableTrackerURL(u) {
				return fmt.Errorf("torrent-set: trackerRemove: torrent %d tracker %d has | or %% in its URL, which the WebAPI can't remove", it.t.Id, id)
			}
			remove = append(remove, u)
		}
		if len(remove) > 0 {
			p.step("torrents/removeTrackers", func() error { return qBTConn.RemoveTorrentTrackers(hash, remove) })
		}
		for _, r := range c.trackerReplace {
			orig, ok := byID[r.id]
			if !ok {
				return fmt.Errorf("torrent-set: trackerReplace: torrent %d has no tracker id %d", it.t.Id, r.id)
			}
			if r.url == "" {
				return errors.New("torrent-set: trackerReplace: empty URL")
			}
			newURL := r.url
			p.step("torrents/editTracker", func() error { return qBTConn.EditTorrentTracker(hash, orig, newURL) })
		}
	}
	return nil
}

var errNoMetadata = errors.New("has no metadata yet, so its files can't be changed")

// planFiles applies Transmission's file commands like libtransmission does:
// files-unwanted, then files-wanted, then priority-high/low/normal. In qBT one
// number holds both: 0 unwanted, 1 low, 6 normal, 7 high (plan.md Decisions).
// A file made wanted keeps its priority if it had one, else gets normal; a
// priority on a file that stays unwanted can't be stored without wanting it,
// so the file stays unwanted. One filePrio call per resulting value.
func (p *torrentSetPlan) planFiles() error {
	files := p.cmds.files
	if len(files) == 0 {
		return nil
	}
	for _, it := range p.items {
		t := it.t
		if isFetchingMetadata(t.State) {
			return fmt.Errorf("torrent-set: torrent %d %w", t.Id, errNoMetadata)
		}
		list, err := qBTConn.GetPropsFiles(t.Hash)
		if isNoMetadata(err) || (err == nil && len(list) == 0) {
			return fmt.Errorf("torrent-set: torrent %d %w", t.Id, errNoMetadata)
		}
		if err != nil {
			return err
		}
		n := len(list)
		indices := func(key string) ([]int, error) {
			ids, ok := files[key]
			if !ok {
				return nil, nil
			}
			if len(ids) == 0 { // rpc-spec 3.2: an empty array means all files
				all := make([]int, n)
				for i := range all {
					all[i] = i
				}
				return all, nil
			}
			for _, id := range ids {
				if id >= n {
					return nil, fmt.Errorf("torrent-set: %s: file index %d out of range (torrent %d has %d files)", key, id, t.Id, n)
				}
			}
			return ids, nil
		}
		wanted := map[int]bool{}
		trPrio := map[int]int{}
		touched := map[int]bool{}
		for _, step := range []struct {
			key  string
			prio int  // qBT priority for priority-* keys
			want bool // for files-* keys
		}{
			{"files-unwanted", 0, false}, {"files-wanted", 0, true},
			{"priority-high", 7, false}, {"priority-low", 1, false}, {"priority-normal", 6, false},
		} {
			ids, err := indices(step.key)
			if err != nil {
				return err
			}
			for _, id := range ids {
				touched[id] = true
				if strings.HasPrefix(step.key, "files-") {
					wanted[id] = step.want
				} else {
					trPrio[id] = step.prio
				}
			}
		}
		ids := make([]int, 0, len(touched))
		for id := range touched {
			ids = append(ids, id)
		}
		sort.Ints(ids)
		byValue := map[int][]int{}
		for _, id := range ids {
			cur := list[id].Priority
			isWanted := cur != 0
			if w, ok := wanted[id]; ok {
				isWanted = w
			}
			value := 0
			switch prio, hasPrio := trPrio[id]; {
			case !isWanted:
				value = 0
			case hasPrio:
				value = prio
			case cur != 0:
				value = cur
			default:
				value = 6
			}
			byValue[value] = append(byValue[value], id)
		}
		values := make([]int, 0, len(byValue))
		for v := range byValue {
			values = append(values, v)
		}
		sort.Ints(values)
		hash, id := t.Hash, t.Id
		for _, v := range values {
			v, fileIDs := v, byValue[v]
			p.step("torrents/filePrio", func() error {
				err := qBTConn.SetFilePriority(hash, fileIDs, v)
				var httpErr *qBT.HTTPError
				if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusConflict {
					return fmt.Errorf("torrent %d: qBittorrent refused the file change (no metadata yet, or a file index is out of range): %w", id, err)
				}
				return err
			})
		}
	}
	return nil
}

// TorrentSetDiff answers torrent-set (see the comment at the top of the file).
func TorrentSetDiff(args json.RawMessage) (JsonMap, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(args, &raw); err != nil {
		return nil, fmt.Errorf("torrent-set: invalid arguments: %w", err)
	}
	req := map[string]json.RawMessage{}
	for k, v := range raw {
		if strings.TrimSpace(string(v)) != "null" {
			req[k] = v
		}
	}
	var ids *json.RawMessage
	if v, ok := req["ids"]; ok {
		ids = &v
	}
	delete(req, "ids")

	state := map[string]interface{}{}
	canon := map[string]json.RawMessage{}
	present := map[string]bool{}
	for key, value := range req {
		switch {
		case isTorrentSetStateKey(key):
			v, err := canonicalStateValue(key, value)
			if err != nil {
				return nil, fmt.Errorf("torrent-set: invalid %s: %w", key, err)
			}
			state[key] = v
			canon[key], _ = json.Marshal(v)
			present[key] = true
		case isTorrentSetCommandKey(key):
			present[key] = true
		default:
			logFieldOnce("torrent-set:"+key, "torrent-set key not in the protocol, ignored")
		}
	}
	cmds, err := parseTorrentSetCommands(req)
	if err != nil {
		return nil, err
	}

	sel, err := parseIDsField(ids)
	if err != nil {
		return nil, err
	}
	if len(sel.torrents) == 0 || len(present) == 0 {
		return JsonMap{}, nil
	}
	items, err := readTorrentSetCurrents(sel.torrents, present)
	if err != nil {
		return nil, err
	}
	currents := make([]JsonMap, len(items))
	for i, it := range items {
		currents[i] = it.cur
	}
	changed := map[string]bool{}
	for _, key := range changedKeys(canon, currents, isTorrentSetStateKey) {
		changed[key] = true
	}

	plan := &torrentSetPlan{items: items, state: state, changed: changed, cmds: cmds, unmappable: map[string]string{}}
	for key := range changed {
		if reason, ok := torrentSetUnmappable[key]; ok {
			plan.unmappable[key] = reason
		}
	}
	planErr := func() error {
		if err := plan.planFiles(); err != nil {
			return err
		}
		if err := plan.planSpeedLimit("downloadLimit", "downloadLimited", qBTConn.SetTorrentsDownloadLimit, "torrents/setDownloadLimit"); err != nil {
			return err
		}
		if err := plan.planSpeedLimit("uploadLimit", "uploadLimited", qBTConn.SetTorrentsUploadLimit, "torrents/setUploadLimit"); err != nil {
			return err
		}
		if err := plan.planShareLimits(); err != nil {
			return err
		}
		plan.planSequential()
		plan.planLabels()
		plan.planTrackerList()
		if err := plan.planTrackerCommands(); err != nil {
			return err
		}
		if err := plan.planLocation(); err != nil {
			return err
		}
		return plan.planQueuePosition()
	}()
	if len(plan.unmappable) > 0 {
		fields := make([]string, 0, len(plan.unmappable))
		for key, reason := range plan.unmappable {
			fields = append(fields, key)
			log.WithField("field", key).WithField("reason", reason).Warn("torrent-set: changed field can't be stored in qBittorrent, nothing applied")
		}
		return nil, unmappableError(fields)
	}
	if planErr != nil {
		log.WithError(planErr).Warn("torrent-set rejected, nothing applied")
		return nil, planErr
	}

	for i, s := range plan.steps {
		if err := s.run(); err != nil {
			if i == 0 {
				return nil, fmt.Errorf("torrent-set: %s failed, nothing was changed: %w", s.what, err)
			}
			return nil, fmt.Errorf("torrent-set: %s failed after %d of %d changes were applied: %w", s.what, i, len(plan.steps), err)
		}
	}
	log.WithField("changed", len(changed)).WithField("calls", len(plan.steps)).Debug("torrent-set applied")
	return JsonMap{}, nil
}
