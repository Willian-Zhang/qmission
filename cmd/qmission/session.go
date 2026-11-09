package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Willian-Zhang/qmission/qBT"
	"github.com/Willian-Zhang/qmission/transmission"
)

func qBTEncryptionToTR(enc int) string {
	switch enc {
	case 0:
		return "preferred"
	case 1:
		return "required"
	default: // 2 = encryption disabled; Transmission has no "off"
		return "tolerated"
	}
}

// qBTSchedulerDaysToTR maps qBT's scheduler_days (src/base/preferences.h
// Scheduler::Days: 0 every day, 1 weekdays, 2 weekends, 3 Monday ... 9 Sunday)
// to Transmission's tr_sched_day bitmask (SUN 1, MON 2, ... SAT 64).
func qBTSchedulerDaysToTR(days int) int {
	switch {
	case days == 0:
		return 127 // TR_SCHED_ALL
	case days == 1:
		return 62 // TR_SCHED_WEEKDAY
	case days == 2:
		return 65 // TR_SCHED_WEEKEND
	case days >= 3 && days <= 8: // Monday..Saturday
		return 1 << uint(days-2)
	case days == 9:
		return 1 // Sunday
	default:
		return 127
	}
}

// qmissionVersion is session-get `version`: the Transmission release matching
// rpc-version, then the qBT version, e.g. "4.1.0 (qmission, qBT 5.3.0)".
func qmissionVersion(appVersion string) string {
	v := strings.TrimPrefix(strings.TrimSpace(appVersion), "v")
	if v == "" {
		return transmission.Version + " (qmission)"
	}
	return fmt.Sprintf("%s (qmission, qBT %s)", transmission.Version, v)
}

// serverState refreshes the sync table (cheap delta) and returns server_state.
// Without -sync it's zero apart from what transfer/info provides.
func serverState() (qBT.ServerState, error) {
	if *useSync {
		if err := qBTConn.UpdateTorrentsList(); err != nil {
			return qBT.ServerState{}, err
		}
		return qBTConn.TorrentsList.ServerState(), nil
	}
	info, err := qBTConn.GetTransferInfo()
	if err != nil {
		return qBT.ServerState{}, err
	}
	state := qBT.ServerState{TransferInfo: info}
	mode, err := qBTConn.Get("transfer/speedLimitsMode", nil)
	if err != nil {
		return qBT.ServerState{}, err
	}
	state.Use_alt_speed_limits = strings.TrimSpace(string(mode)) == "1"
	return state, nil
}

func SessionGet(args json.RawMessage) (JsonMap, error) {
	var req struct {
		Fields []string `json:"fields"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, fmt.Errorf("session-get: invalid arguments: %w", err)
	}

	prefs, err := qBTConn.GetPreferences()
	if err != nil {
		return nil, err
	}
	state, err := serverState()
	if err != nil {
		return nil, err
	}

	session := make(JsonMap, len(transmission.SessionGetBase)+8)
	for key, value := range transmission.SessionGetBase {
		session[key] = value
	}
	session["units"] = transmission.Units()
	session["session-id"] = sessionID
	session["version"] = qmissionVersion(qBTConn.AppVersion())

	// qBT preference limits are B/s with 0 = unlimited; Transmission wants KiB/s (units.speed-bytes).
	session["speed-limit-down"], session["speed-limit-down-enabled"] = bytesToKiB(int64(prefs.Dl_limit))
	session["speed-limit-up"], session["speed-limit-up-enabled"] = bytesToKiB(int64(prefs.Up_limit))
	session["alt-speed-down"], _ = bytesToKiB(int64(prefs.Alt_dl_limit))
	session["alt-speed-up"], _ = bytesToKiB(int64(prefs.Alt_up_limit))
	session["alt-speed-enabled"] = state.Use_alt_speed_limits
	session["alt-speed-time-enabled"] = prefs.Scheduler_enabled
	session["alt-speed-time-begin"] = prefs.Schedule_from_hour*60 + prefs.Schedule_from_min
	session["alt-speed-time-end"] = prefs.Schedule_to_hour*60 + prefs.Schedule_to_min
	session["alt-speed-time-day"] = qBTSchedulerDaysToTR(prefs.Scheduler_days)

	session["download-dir"] = prefs.Save_path
	session["download-dir-free-space"] = state.Free_space_on_disk
	session["incomplete-dir"] = prefs.Temp_path
	session["incomplete-dir-enabled"] = prefs.Temp_path_enabled
	session["rename-partial-files"] = prefs.Incomplete_files_ext
	session["start-added-torrents"] = !prefs.AddStopped()
	session["trash-original-torrent-files"] = prefs.Auto_delete_mode != 0
	if prefs.Disk_cache > 0 {
		session["cache-size-mb"] = prefs.Disk_cache
	}

	session["peer-limit-global"] = prefs.Max_connec
	session["peer-limit-per-torrent"] = prefs.Max_connec_per_torrent
	session["peer-port"] = prefs.Listen_port
	session["peer-port-random-on-start"] = prefs.Random_port
	session["port-forwarding-enabled"] = prefs.Upnp
	session["utp-enabled"] = prefs.Bittorrent_protocol != 1 // 1 = TCP only
	session["dht-enabled"] = prefs.Dht
	session["lpd-enabled"] = prefs.Lsd
	session["pex-enabled"] = prefs.Pex
	session["encryption"] = qBTEncryptionToTR(prefs.Encryption)
	session["blocklist-enabled"] = prefs.Ip_filter_enabled

	session["download-queue-enabled"] = prefs.Queueing_enabled
	session["download-queue-size"] = prefs.Max_active_downloads
	session["seed-queue-enabled"] = prefs.Queueing_enabled
	session["seed-queue-size"] = prefs.Max_active_uploads
	session["queue-stalled-enabled"] = prefs.Dont_count_slow_torrents
	session["queue-stalled-minutes"] = prefs.Slow_torrent_inactive_timer / 60

	session["seedRatioLimit"] = prefs.Max_ratio
	session["seedRatioLimited"] = prefs.Max_ratio_enabled
	session["idle-seeding-limit-enabled"] = prefs.Max_inactive_seeding_time_enabled
	if prefs.Max_inactive_seeding_time > 0 {
		session["idle-seeding-limit"] = prefs.Max_inactive_seeding_time
	}

	session["script-torrent-done-enabled"] = prefs.Autorun_enabled
	session["script-torrent-done-filename"] = prefs.Autorun_program
	session["script-torrent-added-enabled"] = prefs.Autorun_on_torrent_added_enabled
	session["script-torrent-added-filename"] = prefs.Autorun_on_torrent_added_program
	if prefs.Add_trackers_enabled {
		session["default-trackers"] = prefs.Add_trackers
	}

	if len(req.Fields) == 0 {
		return session, nil
	}
	filtered := make(JsonMap, len(req.Fields))
	for _, field := range req.Fields {
		if v, ok := session[field]; ok {
			filtered[field] = v
		} else {
			logFieldOnce("session-get:"+field, "Unknown session-get field, omitted")
		}
	}
	return filtered, nil
}

func SessionStats(args json.RawMessage) (JsonMap, error) {
	state, err := serverState()
	if err != nil {
		return nil, err
	}
	if !*useSync {
		if err := qBTConn.UpdateTorrentsList(); err != nil {
			return nil, err
		}
	}

	all := qBTConn.TorrentsList.Slice()
	active := 0
	for _, torrent := range all {
		if qBTStateToTransmissionStatus(torrent.State, torrent.Progress) != TR_STATUS_STOPPED {
			active++
		}
	}

	current := JsonMap{
		"downloadedBytes": state.Dl_info_data,
		"uploadedBytes":   state.Up_info_data,
		"filesAdded":      0,
		"sessionCount":    1,
		"secondsActive":   0,
	}
	// alltime_* only come with sync/maindata; without it reuse the session numbers.
	cumulative := JsonMap{
		"downloadedBytes": state.Dl_info_data,
		"uploadedBytes":   state.Up_info_data,
		"filesAdded":      0,
		"sessionCount":    1,
		"secondsActive":   0,
	}
	if *useSync {
		cumulative["downloadedBytes"] = state.Alltime_dl
		cumulative["uploadedBytes"] = state.Alltime_ul
	}
	return JsonMap{
		"activeTorrentCount": active,
		"pausedTorrentCount": len(all) - active,
		"torrentCount":       len(all),
		"downloadSpeed":      state.Dl_info_speed,
		"uploadSpeed":        state.Up_info_speed,
		"current-stats":      current,
		"cumulative-stats":   cumulative,
	}, nil
}
