package main

import (
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Willian-Zhang/qmission/qBT"
	log "github.com/sirupsen/logrus"
)

// How long torrent-add waits for the new torrent to show up in sync/maindata.
var (
	addTimeout      = 10 * time.Second
	addPollInterval = 100 * time.Millisecond
)

// torrentAddArgs are the torrent-add arguments (rpc-spec 3.4, classic names).
// TrguiNG also sends keys of its own (`name`, `canRename`, ...); they're ignored.
type torrentAddArgs struct {
	Cookies            *string         `json:"cookies"`
	DownloadDir        *string         `json:"download-dir"`
	Filename           *string         `json:"filename"`
	Metainfo           *string         `json:"metainfo"`
	Labels             []string        `json:"labels"`
	Paused             json.RawMessage `json:"paused"` // boolean; Transmission Remote GUI sends a number
	PeerLimit          json.RawMessage `json:"peer-limit"`
	BandwidthPriority  json.RawMessage `json:"bandwidthPriority"`
	FilesWanted        []int           `json:"files-wanted"`
	FilesUnwanted      []int           `json:"files-unwanted"`
	PriorityHigh       []int           `json:"priority-high"`
	PriorityLow        []int           `json:"priority-low"`
	PriorityNormal     []int           `json:"priority-normal"`
	SequentialDownload json.RawMessage `json:"sequential_download"`
}

// infoHashes identifies a torrent before qBT knows it: hex v1 (SHA-1) and/or
// hex v2 (SHA-256) info hash.
type infoHashes struct {
	v1, v2 string
}

func (h infoHashes) known() bool { return h.v1 != "" || h.v2 != "" }

// addSource is what torrent-add hands to qBT.
type addSource struct {
	metainfo []byte // .torrent content
	url      string // magnet link or http(s) URL
	hashes   infoHashes
	name     string
}

// parseBoolArg reads an optional boolean that some clients send as a number.
func parseBoolArg(raw json.RawMessage, name string) (value, set bool, err error) {
	if len(raw) == 0 || string(raw) == "null" {
		return false, false, nil
	}
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return false, false, fmt.Errorf("invalid %s: %w", name, err)
	}
	switch v := v.(type) {
	case bool:
		return v, true, nil
	case float64:
		return v != 0, true, nil
	}
	return false, false, fmt.Errorf("invalid %s: %s", name, raw)
}

// parseMagnet reads the info hashes of a magnet link: urn:btih (40 hex or 32
// base32 chars) and urn:btmh (multihash SHA-256, "1220" + 64 hex, BEP 52).
func parseMagnet(link string) (infoHashes, string, error) {
	params, err := url.ParseQuery(link[len("magnet:?"):])
	if err != nil {
		return infoHashes{}, "", fmt.Errorf("invalid magnet link: %w", err)
	}
	var h infoHashes
	for _, xt := range params["xt"] {
		lower := strings.ToLower(xt)
		switch {
		case strings.HasPrefix(lower, "urn:btih:"):
			v := xt[len("urn:btih:"):]
			switch len(v) {
			case 40:
				if _, err := hex.DecodeString(v); err != nil {
					return infoHashes{}, "", errors.New("invalid magnet link: bad btih hash")
				}
				h.v1 = strings.ToLower(v)
			case 32:
				raw, err := base32.StdEncoding.DecodeString(strings.ToUpper(v))
				if err != nil {
					return infoHashes{}, "", errors.New("invalid magnet link: bad base32 btih hash")
				}
				h.v1 = hex.EncodeToString(raw)
			default:
				return infoHashes{}, "", errors.New("invalid magnet link: bad btih hash length")
			}
		case strings.HasPrefix(lower, "urn:btmh:"):
			v := lower[len("urn:btmh:"):]
			if _, err := hex.DecodeString(v); err != nil || len(v) != 68 || !strings.HasPrefix(v, "1220") {
				return infoHashes{}, "", errors.New("invalid magnet link: bad btmh hash (want SHA-256 multihash)")
			}
			h.v2 = v[4:]
		}
	}
	if !h.known() {
		return infoHashes{}, "", errors.New("invalid magnet link: no urn:btih or urn:btmh info hash")
	}
	return h, params.Get("dn"), nil
}

func parseAddSource(req *torrentAddArgs) (addSource, error) {
	if req.Metainfo != nil && *req.Metainfo != "" {
		// Some clients wrap the base64 text; Transmission's decoder skips whitespace too.
		clean := strings.Join(strings.Fields(*req.Metainfo), "")
		data, err := base64.StdEncoding.DecodeString(clean)
		if err != nil {
			return addSource{}, fmt.Errorf("torrent-add: invalid metainfo: %w", err)
		}
		v1, v2, name, err := ParseMetainfo(data)
		if err != nil {
			return addSource{}, fmt.Errorf("torrent-add: %w", err)
		}
		return addSource{metainfo: data, hashes: infoHashes{v1, v2}, name: name}, nil
	}
	if req.Filename == nil || *req.Filename == "" {
		return addSource{}, errors.New("torrent-add: no filename or metainfo given")
	}
	filename := strings.TrimSpace(*req.Filename)
	lower := strings.ToLower(filename)
	switch {
	case strings.HasPrefix(lower, "magnet:?"):
		hashes, name, err := parseMagnet(filename)
		if err != nil {
			return addSource{}, fmt.Errorf("torrent-add: %w", err)
		}
		return addSource{url: filename, hashes: hashes, name: name}, nil
	case strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://"):
		return addSource{url: filename}, nil
	}
	return addSource{}, errors.New("torrent-add: filename must be a magnet link or an http(s) URL (send .torrent files as metainfo)")
}

func validateLabels(labels []string) ([]string, error) {
	tags := make([]string, 0, len(labels))
	for _, label := range labels {
		label = strings.TrimSpace(label)
		if label == "" {
			continue
		}
		if strings.Contains(label, ",") {
			return nil, fmt.Errorf("torrent-add: label %q contains a comma, which qBittorrent tags can't hold", label)
		}
		tags = append(tags, label)
	}
	return tags, nil
}

// TorrentAdd adds a magnet link, an http(s) URL (downloaded by qBT) or a base64
// .torrent, then waits (addTimeout) for it to show up so the answer can carry
// its ID. A torrent qBT already has is answered with torrent-duplicate.
func TorrentAdd(args json.RawMessage) (JsonMap, error) {
	var req torrentAddArgs
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, fmt.Errorf("torrent-add: invalid arguments: %w", err)
	}
	paused, pausedSet, err := parseBoolArg(req.Paused, "paused")
	if err != nil {
		return nil, fmt.Errorf("torrent-add: %w", err)
	}
	sequential, _, err := parseBoolArg(req.SequentialDownload, "sequential_download")
	if err != nil {
		return nil, fmt.Errorf("torrent-add: %w", err)
	}
	tags, err := validateLabels(req.Labels)
	if err != nil {
		return nil, err
	}
	for name, set := range map[string]bool{
		"peer-limit":        len(req.PeerLimit) > 0,
		"bandwidthPriority": len(req.BandwidthPriority) > 0,
		"files-wanted":      len(req.FilesWanted) > 0,
		"priority-high":     len(req.PriorityHigh) > 0,
		"priority-low":      len(req.PriorityLow) > 0,
		"priority-normal":   len(req.PriorityNormal) > 0,
	} {
		if set {
			log.WithField("field", name).Debug("torrent-add: ignoring field qBittorrent can't take on add")
		}
	}

	src, err := parseAddSource(&req)
	if err != nil {
		return nil, err
	}
	if len(req.FilesUnwanted) > 0 && src.metainfo == nil {
		return nil, errors.New("torrent-add: files-unwanted needs the torrent's metadata; only supported with metainfo, not with magnet links or URLs")
	}

	if err := qBTConn.UpdateTorrentsList(); err != nil {
		return nil, err
	}
	if src.hashes.known() {
		if t := qBTConn.TorrentsList.FindByInfoHash(src.hashes.v1, src.hashes.v2); t != nil {
			log.WithField("id", t.Id).Info("torrent-add: torrent already in qBittorrent")
			return JsonMap{"torrent-duplicate": addedTorrent(t, src.name)}, nil
		}
	}
	before := map[qBT.Hash]bool{}
	for _, t := range qBTConn.TorrentsList.Slice() {
		before[t.Hash] = true
	}

	params := qBT.AddParams{
		Tags:               tags,
		Sequential:         sequential,
		FirstLastPiecePrio: sequential, // Transmission's sequential mode also fetches the first and last pieces first
	}
	if req.DownloadDir != nil {
		params.SavePath = strings.TrimSpace(*req.DownloadDir)
	}
	if req.Cookies != nil {
		params.Cookie = *req.Cookies
	}
	if src.metainfo != nil {
		params.Torrent = src.metainfo
	} else {
		params.URLs = []string{src.url}
	}
	if pausedSet {
		params.Stopped = &paused
	}
	// With unwanted files, add stopped and start after the priorities are set,
	// so nothing of those files is downloaded in between.
	startAfter := false
	if len(req.FilesUnwanted) > 0 {
		startAfter = !paused
		if !pausedSet {
			prefs, err := qBTConn.GetPreferences()
			if err != nil {
				return nil, err
			}
			startAfter = !prefs.AddStopped()
		}
		stopped := true
		params.Stopped = &stopped
	}

	res, err := qBTConn.AddTorrent(params)
	if errors.Is(err, qBT.ErrAddRejected) && src.hashes.known() {
		// Most likely added by someone else since our check.
		if uErr := qBTConn.UpdateTorrentsList(); uErr == nil {
			if t := qBTConn.TorrentsList.FindByInfoHash(src.hashes.v1, src.hashes.v2); t != nil {
				return JsonMap{"torrent-duplicate": addedTorrent(t, src.name)}, nil
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("torrent-add: %w", err)
	}

	torrent, err := waitForNewTorrent(src, res, before)
	if err != nil {
		return nil, err
	}
	log.WithField("id", torrent.Id).Info("torrent-add: torrent added")

	if len(req.FilesUnwanted) > 0 {
		if err := qBTConn.SetFilePriority(torrent.Hash, req.FilesUnwanted, 0); err != nil {
			return nil, fmt.Errorf("torrent-add: torrent added (id %d, stopped), but setting files-unwanted failed: %w", torrent.Id, err)
		}
		if startAfter {
			if err := qBTConn.StartTorrents([]qBT.Hash{torrent.Hash}); err != nil {
				return nil, fmt.Errorf("torrent-add: torrent added (id %d, stopped), but starting it failed: %w", torrent.Id, err)
			}
		}
	}
	return JsonMap{"torrent-added": addedTorrent(torrent, src.name)}, nil
}

func addedTorrent(t *qBT.TorrentInfo, fallbackName string) JsonMap {
	name := t.Name
	if name == "" {
		name = fallbackName
	}
	return JsonMap{"id": t.Id, "name": name, "hashString": t.Hash}
}

// waitForNewTorrent polls sync/maindata until the added torrent shows up. A
// magnet or .torrent is found by its info hash (or the ID qBT >= 5.x reports);
// a URL, whose hash only qBT learns, by the first hash that wasn't there before.
func waitForNewTorrent(src addSource, res qBT.AddResult, before map[qBT.Hash]bool) (*qBT.TorrentInfo, error) {
	deadline := time.Now().Add(addTimeout)
	for {
		if err := qBTConn.UpdateTorrentsList(); err != nil {
			return nil, err
		}
		if t := findAdded(src, res, before); t != nil {
			return t, nil
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(addPollInterval)
	}
	if src.url != "" && !src.hashes.known() {
		return nil, fmt.Errorf("torrent-add: qBittorrent accepted the URL, but no new torrent appeared within %s "+
			"(still downloading, download failed, or already added)", addTimeout)
	}
	return nil, fmt.Errorf("torrent-add: qBittorrent accepted the torrent, but it did not appear within %s", addTimeout)
}

func findAdded(src addSource, res qBT.AddResult, before map[qBT.Hash]bool) *qBT.TorrentInfo {
	list := &qBTConn.TorrentsList
	if src.hashes.known() {
		if t := list.FindByInfoHash(src.hashes.v1, src.hashes.v2); t != nil {
			return t
		}
	}
	for _, id := range res.AddedIDs {
		if t := list.ByHash(qBT.Hash(strings.ToLower(string(id)))); t != nil {
			return t
		}
	}
	if src.hashes.known() || len(res.AddedIDs) > 0 {
		return nil
	}
	for _, t := range list.Slice() { // ID order: the oldest new torrent first
		if !before[t.Hash] {
			return t
		}
	}
	return nil
}
