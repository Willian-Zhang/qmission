package qBT

import (
	"encoding/json"
	"net/url"
	"strings"
)

// GetPreferencesWithKeys reads app/preferences once and returns it decoded plus
// the raw keys. The key set tells which preferences this qBT version has (e.g.
// max_inactive_seeding_time is 4.6+, add_stopped_enabled 5.x), so a write can
// refuse a key qBT would silently ignore.
func (q *Connection) GetPreferencesWithKeys() (Preferences, map[string]json.RawMessage, error) {
	var pref Preferences
	data, err := q.Get("app/preferences", nil)
	if err != nil {
		return pref, nil, err
	}
	if err := json.Unmarshal(data, &pref); err != nil {
		return pref, nil, decodeError("app/preferences", data, err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return pref, nil, decodeError("app/preferences", data, err)
	}
	return pref, raw, nil
}

// SetPreferences posts app/setPreferences (POST-only) with the given keys as
// the `json` form field. qBT only touches the keys present.
func (q *Connection) SetPreferences(prefs map[string]interface{}) error {
	body, err := json.Marshal(prefs)
	if err != nil {
		return err
	}
	_, err = q.PostForm("app/setPreferences", url.Values{"json": {string(body)}})
	return err
}

// SpeedLimitsMode reports whether the alternative speed limits are on
// (transfer/speedLimitsMode answers "1" or "0").
func (q *Connection) SpeedLimitsMode() (bool, error) {
	data, err := q.Get("transfer/speedLimitsMode", nil)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(data)) == "1", nil
}

// SetSpeedLimitsModeAPIVersion is WebAPI 2.8.14 (qBT 4.5.0), which added
// transfer/setSpeedLimitsMode (qBittorrent commit 7d9790571).
var SetSpeedLimitsModeAPIVersion = APIVersion{2, 8, 14}

// SetAltSpeedLimits turns the alternative speed limits on or off. WebAPI >=
// 2.8.14 has the absolute transfer/setSpeedLimitsMode (`mode` 0|1, POST-only);
// older versions only toggle, so read the mode first and toggle if it differs.
func (q *Connection) SetAltSpeedLimits(on bool) error {
	if _, err := q.ensureLoggedIn(); err != nil {
		return err
	}
	if q.APIVersion().AtLeast(SetSpeedLimitsModeAPIVersion) {
		mode := "0"
		if on {
			mode = "1"
		}
		_, err := q.PostForm("transfer/setSpeedLimitsMode", url.Values{"mode": {mode}})
		return err
	}
	current, err := q.SpeedLimitsMode()
	if err != nil || current == on {
		return err
	}
	return q.ToggleSpeedLimitsMode()
}

// ToggleSpeedLimitsMode flips the alternative speed limits (POST-only).
func (q *Connection) ToggleSpeedLimitsMode() error {
	_, err := q.PostForm("transfer/toggleSpeedLimitsMode", url.Values{})
	return err
}
