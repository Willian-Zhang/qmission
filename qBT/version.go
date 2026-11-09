package qBT

import (
	"fmt"
	"strconv"
	"strings"
)

// APIVersion is qBT's WebAPI version from app/webapiVersion, e.g. "2.9.3".
// qBT drops trailing zero components ("2.11" means 2.11.0).
type APIVersion struct {
	Major, Minor, Patch int
}

var (
	// MinAPIVersion is WebAPI 2.0 = qBittorrent 4.1, the first /api/v2.
	MinAPIVersion = APIVersion{2, 0, 0}
	// StopStartAPIVersion is WebAPI 2.11.0 = qBittorrent 5.0: pause/resume became stop/start.
	StopStartAPIVersion = APIVersion{2, 11, 0}
)

func ParseAPIVersion(s string) (APIVersion, error) {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ".")
	if s == "" || len(parts) > 3 {
		return APIVersion{}, fmt.Errorf("invalid WebAPI version %q", s)
	}
	var nums [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return APIVersion{}, fmt.Errorf("invalid WebAPI version %q", s)
		}
		nums[i] = n
	}
	return APIVersion{nums[0], nums[1], nums[2]}, nil
}

func (v APIVersion) Compare(o APIVersion) int {
	switch {
	case v.Major != o.Major:
		return cmpInt(v.Major, o.Major)
	case v.Minor != o.Minor:
		return cmpInt(v.Minor, o.Minor)
	default:
		return cmpInt(v.Patch, o.Patch)
	}
}

func cmpInt(a, b int) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func (v APIVersion) AtLeast(o APIVersion) bool { return v.Compare(o) >= 0 }

func (v APIVersion) IsZero() bool { return v == APIVersion{} }

func (v APIVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// Gates holds the qBT names that differ between WebAPI < 2.11 (qBT 4.x) and >= 2.11 (qBT 5.x).
type Gates struct {
	StopStart       bool   // true: torrents/stop|start, false: torrents/pause|resume
	StopEndpoint    string // "torrents/stop" or "torrents/pause"
	StartEndpoint   string // "torrents/start" or "torrents/resume"
	StoppedDL       string // torrent state: "stoppedDL" or "pausedDL"
	StoppedUP       string // torrent state: "stoppedUP" or "pausedUP"
	AddStoppedPref  string // app/preferences key: "add_stopped_enabled" or "start_paused_enabled"
	AddStoppedParam string // torrents/add form field: "stopped" or "paused"
}

func GatesFor(v APIVersion) Gates {
	if v.AtLeast(StopStartAPIVersion) {
		return Gates{
			StopStart:       true,
			StopEndpoint:    "torrents/stop",
			StartEndpoint:   "torrents/start",
			StoppedDL:       "stoppedDL",
			StoppedUP:       "stoppedUP",
			AddStoppedPref:  "add_stopped_enabled",
			AddStoppedParam: "stopped",
		}
	}
	return Gates{
		StopStart:       false,
		StopEndpoint:    "torrents/pause",
		StartEndpoint:   "torrents/resume",
		StoppedDL:       "pausedDL",
		StoppedUP:       "pausedUP",
		AddStoppedPref:  "start_paused_enabled",
		AddStoppedParam: "paused",
	}
}

// IsStoppedState reports whether a qBT torrent state is stopped/paused, for either version.
func IsStoppedState(state string) bool {
	switch state {
	case "stoppedDL", "stoppedUP", "pausedDL", "pausedUP":
		return true
	}
	return false
}
