package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/Willian-Zhang/qmission/qBT"
	"github.com/Willian-Zhang/qmission/transmission"
	"github.com/Workiva/go-datastructures/bitarray"
	log "github.com/sirupsen/logrus"
)

// Transmission tr_torrent_activity (transmission.h).
const (
	TR_STATUS_STOPPED       = 0
	TR_STATUS_CHECK_WAIT    = 1
	TR_STATUS_CHECK         = 2
	TR_STATUS_DOWNLOAD_WAIT = 3
	TR_STATUS_DOWNLOAD      = 4
	TR_STATUS_SEED_WAIT     = 5
	TR_STATUS_SEED          = 6
)

// Transmission tr_stat_errtype.
const (
	TR_STAT_OK          = 0
	TR_STAT_LOCAL_ERROR = 3
)

// Transmission TR_ETA_*.
const (
	TR_ETA_NOT_AVAIL = -1
	TR_ETA_UNKNOWN   = -2
)

// qBTMaxETA is qBT's "infinity" ETA (MAX_ETA in src/base/types.h).
const qBTMaxETA = 8640000

// qBittorrent states (5.3.0 serialize_torrent.cpp torrentStateToString; 4.x
// uses pausedDL/pausedUP instead of stoppedDL/stoppedUP; "allocating" is from
// older releases).
func qBTStateToTransmissionStatus(state string, progress float64) int {
	switch state {
	case "stoppedDL", "stoppedUP", "pausedDL", "pausedUP", "error", "missingFiles", "unknown":
		return TR_STATUS_STOPPED
	case "checkingResumeData":
		return TR_STATUS_CHECK_WAIT
	case "checkingDL", "checkingUP":
		return TR_STATUS_CHECK
	case "queuedDL":
		return TR_STATUS_DOWNLOAD_WAIT
	case "downloading", "stalledDL", "forcedDL", "metaDL", "forcedMetaDL", "allocating":
		return TR_STATUS_DOWNLOAD
	case "queuedUP":
		return TR_STATUS_SEED_WAIT
	case "uploading", "stalledUP", "forcedUP":
		return TR_STATUS_SEED
	case "moving":
		if progress >= 1 {
			return TR_STATUS_SEED
		}
		return TR_STATUS_DOWNLOAD
	default:
		return TR_STATUS_STOPPED
	}
}

// qBTStateToTransmissionError returns Transmission's error/errorString.
// qBT's WebAPI has no error message for a torrent, so the string is generic.
func qBTStateToTransmissionError(state string) (int, string) {
	switch state {
	case "error":
		return TR_STAT_LOCAL_ERROR, "qBittorrent reports an error for this torrent (see the qBittorrent log)"
	case "missingFiles":
		return TR_STAT_LOCAL_ERROR, "Files are missing"
	default:
		return TR_STAT_OK, ""
	}
}

func qBTStateToTransmissionStalled(state string) bool {
	return state == "stalledDL" || state == "stalledUP"
}

func isFetchingMetadata(state string) bool {
	return state == "metaDL" || state == "forcedMetaDL"
}

// mapETA: qBT sends MAX_ETA for "infinite"/not applicable. Transmission sends
// TR_ETA_UNKNOWN (-2) while downloading without speed and TR_ETA_NOT_AVAIL (-1)
// otherwise (libtransmission/torrent.cc tr_torrent::stats).
func mapETA(eta int64, status int) int64 {
	switch {
	case eta < 0:
		return TR_ETA_NOT_AVAIL
	case eta >= qBTMaxETA:
		if status == TR_STATUS_DOWNLOAD {
			return TR_ETA_UNKNOWN
		}
		return TR_ETA_NOT_AVAIL
	default:
		return eta
	}
}

// mapDate: qBT reports an unset date as -1, 4294967295 (old releases) or a
// timezone-shifted invalid QDateTime (e.g. -28800 in 4.6.4). Transmission uses 0.
func mapDate(t int64) int64 {
	if t <= 0 || t >= math.MaxUint32 {
		return 0
	}
	return t
}

// parseTags splits qBT's comma-separated tags into Transmission labels.
func parseTags(tags string) []string {
	labels := make([]string, 0)
	for _, tag := range strings.Split(tags, ",") {
		if tag = strings.TrimSpace(tag); tag != "" {
			labels = append(labels, tag)
		}
	}
	return labels
}

// bytesToKiB converts a qBT limit (B/s, 0 or -1 = unlimited) to Transmission's
// KiB/s value and enabled flag.
func bytesToKiB(limit int64) (int64, bool) {
	if limit <= 0 {
		return 0, false
	}
	kib := (limit + 512) / 1024
	if kib < 1 {
		kib = 1
	}
	return kib, true
}

// queuePositions maps qBT queue positions to Transmission's [0, n).
// qBT `priority` is 1-based for queued torrents and 0 for torrents outside the
// queue (seeding, or queueing disabled); Transmission has one queue holding every
// torrent. Queued torrents come first in qBT order, then the rest by ID.
func queuePositions(all qBT.TorrentInfoList) map[qBT.Hash]int {
	queued := make(qBT.TorrentInfoList, 0, len(all))
	rest := make(qBT.TorrentInfoList, 0, len(all))
	for _, t := range all {
		if t.Priority > 0 {
			queued = append(queued, t)
		} else {
			rest = append(rest, t)
		}
	}
	sort.SliceStable(queued, func(i, j int) bool {
		if queued[i].Priority != queued[j].Priority {
			return queued[i].Priority < queued[j].Priority
		}
		return queued[i].Id < queued[j].Id
	})
	sort.SliceStable(rest, func(i, j int) bool { return rest[i].Id < rest[j].Id })
	positions := make(map[qBT.Hash]int, len(all))
	for i, t := range append(queued, rest...) {
		positions[t.Hash] = i
	}
	return positions
}

// MapTorrentList fills every field that comes from torrents/info or
// sync/maindata, so list polls need no per-torrent request.
func MapTorrentList(dst JsonMap, src *qBT.TorrentInfo) {
	status := qBTStateToTransmissionStatus(src.State, src.Progress)
	errCode, errString := qBTStateToTransmissionError(src.State)

	dst["id"] = src.Id
	dst["hashString"] = src.Hash
	dst["name"] = EscapeString(src.Name)
	dst["addedDate"] = mapDate(src.Added_on)
	dst["startDate"] = mapDate(src.Added_on) // qBT has no start time
	dst["doneDate"] = mapDate(src.Completion_on)
	dst["activityDate"] = mapDate(src.Last_activity)
	dst["sizeWhenDone"] = maxInt64(src.Size, 0)
	dst["totalSize"] = maxInt64(src.Total_size, 0)
	dst["leftUntilDone"] = maxInt64(src.Amount_left, 0)
	dst["haveValid"] = maxInt64(src.Completed, 0)
	dst["haveUnchecked"] = 0
	dst["downloadDir"] = EscapeString(src.Save_path)
	dst["rateDownload"] = src.Dlspeed
	dst["rateUpload"] = src.Upspeed
	dst["uploadRatio"] = src.Ratio
	dst["eta"] = mapETA(src.Eta, status)
	dst["status"] = status
	if status == TR_STATUS_CHECK {
		dst["recheckProgress"] = src.Progress
	} else {
		dst["recheckProgress"] = 0
	}
	dst["error"] = errCode
	dst["errorString"] = errString
	dst["isStalled"] = qBTStateToTransmissionStalled(src.State)
	dst["percentDone"] = src.Progress
	if src.Total_size > 0 {
		dst["percentComplete"] = math.Min(math.Max(float64(src.Completed)/float64(src.Total_size), 0), 1)
	} else {
		dst["percentComplete"] = 0
	}
	// Approximation: qBT's availability is distributed copies (-1 when not downloading).
	switch {
	case src.Availability >= 1:
		dst["desiredAvailable"] = maxInt64(src.Amount_left, 0)
	case src.Availability > 0:
		dst["desiredAvailable"] = int64(float64(maxInt64(src.Amount_left, 0)) * src.Availability)
	default:
		dst["desiredAvailable"] = 0
	}
	dst["peersGettingFromUs"] = src.Num_leechs
	dst["peersSendingToUs"] = src.Num_seeds
	dst["peersConnected"] = src.Num_seeds + src.Num_leechs
	dst["downloadedEver"] = maxInt64(src.Downloaded, 0)
	dst["uploadedEver"] = maxInt64(src.Uploaded, 0)
	dst["downloadLimit"], dst["downloadLimited"] = bytesToKiB(src.Dl_limit)
	dst["uploadLimit"], dst["uploadLimited"] = bytesToKiB(src.Up_limit)
	switch {
	case src.Ratio_limit == -2: // use the global limit
		dst["seedRatioMode"], dst["seedRatioLimit"] = 0, 0
	case src.Ratio_limit < 0: // unlimited
		dst["seedRatioMode"], dst["seedRatioLimit"] = 2, 0
	default:
		dst["seedRatioMode"], dst["seedRatioLimit"] = 1, src.Ratio_limit
	}
	dst["secondsSeeding"] = maxInt64(src.Seeding_time, 0)
	dst["secondsDownloading"] = maxInt64(src.Time_active-src.Seeding_time, 0)
	if isFetchingMetadata(src.State) {
		dst["metadataPercentComplete"] = 0
	} else {
		dst["metadataPercentComplete"] = 1
	}
	dst["labels"] = parseTags(src.Tags)
	dst["sequential_download"] = src.Seq_dl
	dst["magnetLink"] = src.Magnet_uri
	if src.Private {
		dst["isPrivate"] = true // qBT 5.x only; 4.x comes from the props cache
	}
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// trackerDefaultPorts: libtransmission/web-utils.cc getPortForScheme, plus ws/wss.
var trackerDefaultPorts = map[string]string{
	"ftp": "21", "http": "80", "https": "443", "sftp": "22", "udp": "80", "ws": "80", "wss": "443",
}

// trackerHost returns Transmission 4.x's trackerStats `host` ("host:port", no
// scheme; announcer.cc trackerView) and `sitename`.
func trackerHost(announce string) (host string, sitename string) {
	u, err := url.Parse(announce)
	if err != nil || u.Hostname() == "" {
		return announce, ""
	}
	hostname := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		port = trackerDefaultPorts[strings.ToLower(u.Scheme)]
	}
	if port == "" {
		host = hostname
	} else {
		host = net.JoinHostPort(hostname, port)
	}
	return host, siteName(hostname)
}

// secondLevelSuffixes approximates the public-suffix list Transmission uses
// (libpsl) for the common "co.uk"-style registries.
var secondLevelSuffixes = map[string]bool{
	"ac": true, "co": true, "com": true, "edu": true, "gov": true, "net": true, "org": true, "or": true, "ne": true,
}

// siteName: "www.example.co.uk" -> "example", IPs unchanged (web-utils.cc getSiteName).
func siteName(hostname string) string {
	if net.ParseIP(hostname) != nil {
		return hostname
	}
	labels := strings.Split(strings.TrimSuffix(hostname, "."), ".")
	n := len(labels)
	switch {
	case n >= 3 && len(labels[n-1]) == 2 && secondLevelSuffixes[labels[n-2]]:
		return labels[n-3]
	case n >= 2:
		return labels[n-2]
	default:
		return hostname
	}
}

// mapSyntheticTrackers fills trackers/trackerStats/trackerList for list polls
// from torrents/info alone: one entry for qBT's current tracker with the swarm
// counts (TrguiNG's tracker column only reads trackerStats[0].host). A torrent with trackers but none working gets an entry without a URL,
// so the seed/peer totals still show; one with no trackers gets none.
func mapSyntheticTrackers(dst JsonMap, src *qBT.TorrentInfo) {
	if src.Tracker == "" && src.Trackers_count == 0 {
		dst["trackers"] = []JsonMap{}
		dst["trackerStats"] = []JsonMap{}
		dst["trackerList"] = ""
		return
	}
	stat := transmission.TrackerStatsTemplate()
	stat["seederCount"] = src.Num_complete
	stat["leecherCount"] = src.Num_incomplete
	trackers := []JsonMap{}
	if src.Tracker != "" {
		host, site := trackerHost(src.Tracker)
		stat["announce"] = src.Tracker
		stat["host"] = host
		stat["sitename"] = site
		// No announce state: 4.6.4's `tracker` is the last working tracker, but
		// 5.3.0 falls back to the first tracker (TorrentImpl::currentTracker), so a
		// URL doesn't mean it works. hasAnnounced=false keeps TrguiNG's tracker
		// status blank instead of showing a made-up "Working" or error.
		stat["announceState"] = 1 // TR_TRACKER_WAITING
		trackers = append(trackers, JsonMap{"announce": src.Tracker, "id": 0, "scrape": "", "sitename": site, "tier": 0})
	}
	dst["trackers"] = trackers
	dst["trackerStats"] = []JsonMap{JsonMap(stat)}
	dst["trackerList"] = src.Tracker
}

func trackerTier(tier interface{}) (int, bool) {
	switch v := tier.(type) {
	case float64:
		return int(v), v >= 0
	default:
		return 0, false
	}
}

// MapPropsTrackers maps torrents/trackers (single-ID requests) to trackers,
// trackerStats and trackerList, skipping the DHT/PeX/LSD pseudo-entries.
func MapPropsTrackers(dst JsonMap, trackers []qBT.PropertiesTrackers) {
	trackersList := make([]JsonMap, 0, len(trackers))
	stats := make([]JsonMap, 0, len(trackers))
	var tiers [][]string
	tierIndex := map[int]int{}
	id := 0
	for _, tr := range trackers {
		if strings.HasPrefix(tr.Url, "** [") {
			continue
		}
		tier, _ := trackerTier(tr.Tier)
		host, site := trackerHost(tr.Url)
		trackersList = append(trackersList, JsonMap{
			"announce": tr.Url, "id": id, "scrape": "", "sitename": site, "tier": tier,
		})

		stat := transmission.TrackerStatsTemplate()
		stat["announce"] = tr.Url
		stat["host"] = host
		stat["sitename"] = site
		stat["id"] = id
		stat["tier"] = tier
		stat["seederCount"] = tr.Num_seeds
		stat["leecherCount"] = tr.Num_leeches
		stat["downloadCount"] = tr.Num_downloaded
		stat["lastAnnouncePeerCount"] = maxInt64(int64(tr.Num_peers), 0)
		// qBT tracker status: 0 disabled, 1 not contacted, 2 working, 3 updating,
		// 4 not working; 5.x adds 5 tracker error, 6 unreachable.
		switch tr.Status {
		case 0:
			stat["announceState"] = 0 // TR_TRACKER_INACTIVE
		case 1:
			stat["announceState"] = 1 // TR_TRACKER_WAITING
		case 2:
			stat["announceState"] = 1
			stat["hasAnnounced"] = true
			stat["lastAnnounceSucceeded"] = true
			stat["lastAnnounceResult"] = "Success"
		case 3:
			stat["announceState"] = 3 // TR_TRACKER_ACTIVE
		default:
			stat["announceState"] = 1
			stat["hasAnnounced"] = true
			stat["lastAnnounceSucceeded"] = false
			if tr.Msg != "" {
				stat["lastAnnounceResult"] = tr.Msg
			} else {
				stat["lastAnnounceResult"] = "Tracker is not working"
			}
		}
		stats = append(stats, JsonMap(stat))

		idx, ok := tierIndex[tier]
		if !ok {
			idx = len(tiers)
			tierIndex[tier] = idx
			tiers = append(tiers, nil)
		}
		tiers[idx] = append(tiers[idx], tr.Url)
		id++
	}
	tierStrings := make([]string, len(tiers))
	for i, urls := range tiers {
		tierStrings[i] = strings.Join(urls, "\n")
	}
	dst["trackers"] = trackersList
	dst["trackerStats"] = stats
	dst["trackerList"] = strings.Join(tierStrings, "\n\n")
}

func MapPieceStates(dst JsonMap, pieces []byte) {
	bits := bitarray.NewSparseBitArray()
	for i, value := range pieces {
		if value == 2 {
			bits.SetBit(uint64(i))
		}
	}
	serialized, _ := bitarray.Marshal(bits) // TODO(stage 1b): drops the header (h31 PR #6)
	dst["pieces"] = base64.StdEncoding.EncodeToString(serialized)
}

// MapPropsGeneral maps torrents/properties for single-ID requests.
// haveValid, limits and counters stay from the list mapping (maindata is fresher).
func MapPropsGeneral(dst JsonMap, propGeneral qBT.PropertiesGeneral) {
	dst["pieceSize"] = propGeneral.Piece_size
	dst["pieceCount"] = maxInt64(int64(propGeneral.Pieces_num), 0)
	dst["dateCreated"] = mapDate(propGeneral.Creation_date)
	dst["creator"] = EscapeString(propGeneral.Created_by)
	dst["comment"] = EscapeString(propGeneral.Comment)
	dst["isPrivate"] = propGeneral.Is_private
	dst["corruptEver"] = propGeneral.Total_wasted
	dst["peersFrom"] = JsonMap{
		"fromCache": 0, "fromDht": 0, "fromIncoming": 0, "fromLpd": 0, "fromLtep": 0, "fromPex": 0,
		"fromTracker": propGeneral.Peers,
	}
	dst["maxConnectedPeers"] = propGeneral.Nb_connections_limit
	dst["peer-limit"] = propGeneral.Nb_connections_limit
}

func MapPropsPeers(dst JsonMap, hash qBT.Hash) error {
	torrents, err := qBTConn.Get("sync/torrentPeers", map[string]string{"hash": string(hash), "rid": "0"})
	if err != nil {
		return err
	}
	log.Debug(string(torrents))
	var resp struct {
		Peers map[string]qBT.PeerInfo
	}
	if err := json.Unmarshal(torrents, &resp); err != nil {
		return fmt.Errorf("qBittorrent sync/torrentPeers: decoding response: %w", err)
	}
	trPeers := make([]transmission.PeerInfo, 0, len(resp.Peers))
	for _, peer := range resp.Peers {
		trPeers = append(trPeers, transmission.PeerInfo{
			RateToPeer:   peer.Up_speed,
			RateToClient: peer.Dl_speed,
			ClientName:   EscapeString(peer.Client),
			FlagStr:      peer.Flags,
			Country:      EscapeString(peer.Country),
			Address:      peer.IP,
			Progress:     peer.Progress,
			Port:         peer.Port,
		})
	}
	dst["peers"] = trPeers
	return nil
}

func MapPropsFiles(dst JsonMap, filesInfo []qBT.PropertiesFiles) {
	fileNum := len(filesInfo)
	files := make([]JsonMap, fileNum)
	fileStats := make([]JsonMap, fileNum)
	priorities := make([]int, fileNum)
	wanted := make([]int, fileNum)
	for i, value := range filesInfo {
		bytesCompleted := int64(float64(value.Size) * value.Progress)
		files[i] = JsonMap{
			"bytesCompleted": bytesCompleted,
			"length":         value.Size,
			"name":           EscapeString(value.Name),
		}
		fileStats[i] = JsonMap{
			"bytesCompleted": bytesCompleted,
			"wanted":         value.Priority != 0,
			"priority":       0, // TODO(stage 1b): Xmister read mapping
		}
		if value.Priority != 0 {
			wanted[i] = 1
		}
	}
	dst["files"] = files
	dst["fileStats"] = fileStats
	dst["priorities"] = priorities
	dst["wanted"] = wanted
	dst["file-count"] = fileNum
}

// staticProps are the torrents/properties values that never change once a
// torrent has metadata. They're cached per hash for the life of the process so
// list polls don't call torrents/properties per torrent. file-count (from
// torrents/files) is cached the same way, only when a client asks for it.
type staticProps struct {
	pieceCount  int
	pieceSize   int
	dateCreated int64
	isPrivate   bool
}

var staticCache = struct {
	sync.Mutex
	props     map[qBT.Hash]staticProps
	fileCount map[qBT.Hash]int
}{props: map[qBT.Hash]staticProps{}, fileCount: map[qBT.Hash]int{}}

// staticFetchParallelism bounds concurrent per-torrent calls when the cache is
// cold (first poll after start).
const staticFetchParallelism = 8

func resetCaches() {
	staticCache.Lock()
	staticCache.props = map[qBT.Hash]staticProps{}
	staticCache.fileCount = map[qBT.Hash]int{}
	staticCache.Unlock()
	loggedFields = sync.Map{}
}

func storeStaticProps(hash qBT.Hash, p qBT.PropertiesGeneral) {
	if p.Pieces_num <= 0 {
		return // no metadata yet
	}
	staticCache.Lock()
	staticCache.props[hash] = staticProps{
		pieceCount:  p.Pieces_num,
		pieceSize:   p.Piece_size,
		dateCreated: mapDate(p.Creation_date),
		isPrivate:   p.Is_private,
	}
	staticCache.Unlock()
}

func storeFileCount(hash qBT.Hash, files []qBT.PropertiesFiles) {
	if len(files) == 0 {
		return // no metadata yet
	}
	staticCache.Lock()
	staticCache.fileCount[hash] = len(files)
	staticCache.Unlock()
}

// fetchParallel runs fetch for every hash with bounded parallelism. Errors are
// logged, not returned: a list poll shouldn't fail for one torrent.
func fetchParallel(hashes []qBT.Hash, what string, fetch func(qBT.Hash) error) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	failures := 0
	sem := make(chan struct{}, staticFetchParallelism)
	for _, hash := range hashes {
		wg.Add(1)
		sem <- struct{}{}
		go func(hash qBT.Hash) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := fetch(hash); err != nil {
				mu.Lock()
				failures++
				mu.Unlock()
				log.WithError(err).Debug(what + " failed")
			}
		}(hash)
	}
	wg.Wait()
	if failures > 0 {
		log.Warnf("%s failed for %d of %d torrents; retrying on the next poll", what, failures, len(hashes))
	}
}

// getStaticProps returns the cached static props (and file counts if wanted) for
// torrents with metadata, fetching the missing ones first. Torrents still
// fetching metadata are skipped: their pieceCount stays 0, which TrguiNG shows
// as "Magnetizing".
func getStaticProps(torrents qBT.TorrentInfoList, wantProps, wantFileCount bool) (map[qBT.Hash]staticProps, map[qBT.Hash]int) {
	var propMisses, fileMisses []qBT.Hash
	staticCache.Lock()
	for _, t := range torrents {
		if isFetchingMetadata(t.State) {
			continue
		}
		if _, ok := staticCache.props[t.Hash]; wantProps && !ok {
			propMisses = append(propMisses, t.Hash)
		}
		if _, ok := staticCache.fileCount[t.Hash]; wantFileCount && !ok {
			fileMisses = append(fileMisses, t.Hash)
		}
	}
	staticCache.Unlock()

	if len(propMisses) > 0 {
		fetchParallel(propMisses, "torrents/properties", func(hash qBT.Hash) error {
			props, err := qBTConn.GetPropsGeneral(hash)
			if err == nil {
				storeStaticProps(hash, props)
			}
			return err
		})
	}
	if len(fileMisses) > 0 {
		fetchParallel(fileMisses, "torrents/files", func(hash qBT.Hash) error {
			files, err := qBTConn.GetPropsFiles(hash)
			if err == nil {
				storeFileCount(hash, files)
			}
			return err
		})
	}

	props := make(map[qBT.Hash]staticProps, len(torrents))
	fileCounts := make(map[qBT.Hash]int, len(torrents))
	staticCache.Lock()
	for _, t := range torrents {
		if isFetchingMetadata(t.State) {
			continue
		}
		if p, ok := staticCache.props[t.Hash]; ok && wantProps {
			props[t.Hash] = p
		}
		if n, ok := staticCache.fileCount[t.Hash]; ok && wantFileCount {
			fileCounts[t.Hash] = n
		}
	}
	staticCache.Unlock()
	return props, fileCounts
}

// loggedFields remembers which unmapped field names were already logged.
var loggedFields sync.Map

func logFieldOnce(field, msg string) {
	if _, loaded := loggedFields.LoadOrStore(field, struct{}{}); !loaded {
		log.WithField("field", field).Debug(msg)
	}
}

type fieldNeeds struct {
	static, fileCount, props, trackers, files, pieces, peers, queue bool
}

func classifyFields(fields []string) (n fieldNeeds) {
	for _, field := range fields {
		switch field {
		case "pieceCount", "pieceSize", "dateCreated", "isPrivate":
			n.static = true
		case "comment", "creator", "corruptEver", "peersFrom", "maxConnectedPeers", "peer-limit":
			n.props = true
		case "trackers", "trackerStats", "trackerList":
			n.trackers = true
		case "file-count":
			n.fileCount = true
		case "files", "fileStats", "priorities", "wanted":
			n.files = true
		case "pieces":
			n.pieces = true
		case "peers":
			n.peers = true
		case "queuePosition":
			n.queue = true
		}
	}
	return
}

// TorrentGet answers torrent-get. List polls (no ids, several ids, or
// recently-active) are served from the torrent list plus the static props cache.
// A request for exactly one torrent by ID/hash is a details request: it also
// reads torrents/properties and the real trackers. files/pieces/peers are read
// whenever they're asked for.
func TorrentGet(args json.RawMessage) (JsonMap, error) {
	var req transmission.GetRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, fmt.Errorf("torrent-get: invalid arguments: %w", err)
	}
	if len(req.Fields) == 0 {
		return nil, errors.New("torrent-get: no fields specified")
	}
	if req.Format != "" && req.Format != "objects" && req.Format != "table" {
		return nil, fmt.Errorf("torrent-get: invalid format %q", req.Format)
	}

	sel, err := parseIDsField(req.Ids)
	if err != nil {
		return nil, err
	}
	torrents := sel.torrents
	details := sel.explicit && !sel.recentlyActive && len(torrents) == 1
	needs := classifyFields(req.Fields)
	if len(torrents) > 1 && (needs.files || needs.pieces || needs.peers) {
		log.Info("torrent-get asked for files/pieces/peers of several torrents (one request per torrent)")
	}

	var positions map[qBT.Hash]int
	if needs.queue {
		positions = queuePositions(qBTConn.TorrentsList.Slice())
	}
	var static map[qBT.Hash]staticProps
	var fileCounts map[qBT.Hash]int
	if (needs.static && !details) || (needs.fileCount && !needs.files) {
		static, fileCounts = getStaticProps(torrents, needs.static && !details, needs.fileCount && !needs.files)
	}

	defaults := transmission.TorrentGetDefaults()
	resultList := make([]JsonMap, 0, len(torrents))
	for _, torrentItem := range torrents {
		hash := torrentItem.Hash
		translated := make(JsonMap)
		MapTorrentList(translated, torrentItem)
		if positions != nil {
			translated["queuePosition"] = positions[hash]
		}
		if p, ok := static[hash]; ok {
			translated["pieceCount"] = p.pieceCount
			translated["pieceSize"] = p.pieceSize
			translated["dateCreated"] = p.dateCreated
			if p.isPrivate {
				translated["isPrivate"] = true
			}
		}
		if n, ok := fileCounts[hash]; ok {
			translated["file-count"] = n
		}

		if details && (needs.props || needs.static) && !isFetchingMetadata(torrentItem.State) {
			propGeneral, err := qBTConn.GetPropsGeneral(hash)
			if err != nil {
				return nil, err
			}
			storeStaticProps(hash, propGeneral)
			MapPropsGeneral(translated, propGeneral)
		}
		if needs.trackers {
			if details {
				trackers, err := qBTConn.GetPropsTrackers(hash)
				if err != nil {
					return nil, err
				}
				MapPropsTrackers(translated, trackers)
			} else {
				mapSyntheticTrackers(translated, torrentItem)
			}
		}
		if needs.pieces {
			pieces, err := qBTConn.GetPiecesStates(hash)
			if err != nil {
				return nil, err
			}
			MapPieceStates(translated, pieces)
		}
		if needs.files {
			files, err := qBTConn.GetPropsFiles(hash)
			if err != nil {
				return nil, err
			}
			storeFileCount(hash, files)
			MapPropsFiles(translated, files)
		}
		if needs.peers {
			if err := MapPropsPeers(translated, hash); err != nil {
				return nil, err
			}
		}

		out := make(JsonMap, len(req.Fields))
		for _, field := range req.Fields {
			if v, ok := translated[field]; ok {
				out[field] = v
			} else if v, ok := defaults[field]; ok {
				out[field] = v
				logFieldOnce(field, "torrent-get field not mapped from qBittorrent, answering a template value")
			} else {
				logFieldOnce(field, "Unknown torrent-get field, omitted")
			}
		}
		resultList = append(resultList, out)
	}

	response := JsonMap{}
	if req.Format == "table" {
		response["torrents"] = toTable(req.Fields, resultList)
	} else {
		response["torrents"] = resultList
	}
	if sel.recentlyActive {
		response["removed"] = qBTConn.TorrentsList.GetRemoved()
	}
	return response, nil
}

// toTable builds torrent-get's "table" format: a header row of keys, then one
// row of values per torrent.
func toTable(fields []string, objects []JsonMap) [][]interface{} {
	var keys []string
	seen := map[string]bool{}
	for _, f := range fields {
		if seen[f] {
			continue
		}
		seen[f] = true
		if len(objects) == 0 {
			keys = append(keys, f)
		} else if _, ok := objects[0][f]; ok {
			keys = append(keys, f)
		}
	}
	header := make([]interface{}, len(keys))
	for i, k := range keys {
		header[i] = k
	}
	table := [][]interface{}{header}
	for _, obj := range objects {
		row := make([]interface{}, len(keys))
		for i, k := range keys {
			row[i] = obj[k]
		}
		table = append(table, row)
	}
	return table
}
