package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// The diff rule for torrent-set and session-set (scrachpad/plan.md, Decisions):
// TrguiNG sends the whole form back, read-only fields included. A field counts
// as changed only if it differs from a fresh read mapped to Transmission terms;
// for multi-torrent torrent-set it's unchanged if it equals the current value
// of at least one targeted torrent. Changed unmappable fields fail the request
// before anything is applied.

// normalizeJSON round-trips v through JSON so 1, 1.0, int64(1) and json.Number
// all compare equal, and structs compare like the maps they marshal to.
func normalizeJSON(v interface{}) interface{} {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out interface{}
	if err := json.Unmarshal(b, &out); err != nil {
		return v
	}
	return out
}

// sameJSONValue reports whether a request value equals a current value.
func sameJSONValue(req json.RawMessage, current interface{}) bool {
	var r interface{}
	if err := json.Unmarshal(req, &r); err != nil {
		return false
	}
	return reflect.DeepEqual(r, normalizeJSON(current))
}

// changedKeys returns the request keys (sorted) that `isProtocolKey` accepts and
// whose value differs from every map in currents. A key missing from a current
// map counts as different for that map. Keys that aren't in the protocol are
// ignored, as the rule says.
func changedKeys(req map[string]json.RawMessage, currents []JsonMap, isProtocolKey func(string) bool) []string {
	var changed []string
	for key, val := range req {
		if isProtocolKey != nil && !isProtocolKey(key) {
			continue
		}
		same := false
		for _, cur := range currents {
			if v, ok := cur[key]; ok && sameJSONValue(val, v) {
				same = true
				break
			}
		}
		if !same {
			changed = append(changed, key)
		}
	}
	sort.Strings(changed)
	return changed
}

// unmappableError is the error result when changed fields can't be stored in
// qBittorrent. Nothing is applied when it's returned.
func unmappableError(fields []string) error {
	sorted := append([]string(nil), fields...)
	sort.Strings(sorted)
	return fmt.Errorf("qBittorrent can't store these fields, nothing was changed: %s", strings.Join(sorted, ", "))
}
