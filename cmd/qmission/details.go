package main

// torrent-get fields that need per-torrent qBT endpoints: files, trackers,
// peers, pieces and webseeds (stage 1b details pane). TorrentGet decides which
// endpoints a request needs (planDetails), fetches each at most once per torrent
// (fetchDetails) and maps the answers (applyDetails).

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"mime"
	"net/http"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/Willian-Zhang/qmission/qBT"
	"github.com/Willian-Zhang/qmission/transmission"
	log "github.com/sirupsen/logrus"
)

// Transmission tr_priority_t (transmission.h).
const (
	TR_PRI_LOW    = -1
	TR_PRI_NORMAL = 0
	TR_PRI_HIGH   = 1
)

// detailPlan says which per-torrent endpoints one torrent-get entry needs.
type detailPlan struct {
	props, trackers, files, pieces, peers, webseeds bool
}

// planDetails: props, real trackers, peersFrom, primary-mime-type and webseeds
// only for details requests (one torrent by explicit id); files, pieces and
// peers whenever they're asked for. A torrent still fetching metadata has no
// props, files or pieces to read, so those are mapped as empty without a call.
func planDetails(t *qBT.TorrentInfo, n fieldNeeds, details bool) detailPlan {
	return detailPlan{
		props:    details && (n.props || n.static) && !isFetchingMetadata(t.State),
		trackers: details && n.trackers,
		files:    n.files || (details && n.mime),
		pieces:   n.pieces,
		peers:    n.peers || (details && n.peersFrom),
		webseeds: details && n.webseeds,
	}
}

// detailData holds the endpoint answers for one torrent.
type detailData struct {
	props    qBT.PropertiesGeneral
	trackers []qBT.PropertiesTrackers
	files    []qBT.PropertiesFiles
	pieces   []byte
	peers    map[string]qBT.PeerInfo
	webseeds []string
}

// isNoMetadata: qBT 5.x answers 409 Conflict on some per-file endpoints while a
// torrent has no metadata; reads are treated as empty then.
func isNoMetadata(err error) bool {
	var httpErr *qBT.HTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusConflict
}

// fetchDetails calls each planned endpoint once, concurrently. The first error
// (in a fixed endpoint order) is returned; webseeds failures are only logged.
func fetchDetails(t *qBT.TorrentInfo, plan detailPlan) (*detailData, error) {
	hash := t.Hash
	noMeta := isFetchingMetadata(t.State)
	d := &detailData{}
	var errProps, errTrackers, errFiles, errPieces, errPeers error
	var wg sync.WaitGroup
	run := func(want bool, fn func()) {
		if want {
			wg.Add(1)
			go func() { defer wg.Done(); fn() }()
		}
	}
	run(plan.props, func() { d.props, errProps = qBTConn.GetPropsGeneral(hash) })
	run(plan.trackers, func() { d.trackers, errTrackers = qBTConn.GetPropsTrackers(hash) })
	run(plan.files && !noMeta, func() {
		d.files, errFiles = qBTConn.GetPropsFiles(hash)
		if isNoMetadata(errFiles) {
			d.files, errFiles = nil, nil
		}
	})
	run(plan.pieces && !noMeta, func() {
		d.pieces, errPieces = qBTConn.GetPiecesStates(hash)
		if isNoMetadata(errPieces) {
			d.pieces, errPieces = nil, nil
		}
	})
	run(plan.peers, func() { d.peers, errPeers = getTorrentPeers(hash) })
	run(plan.webseeds, func() {
		var err error
		if d.webseeds, err = getWebseeds(hash); err != nil {
			log.WithError(err).Warn("torrents/webseeds failed; answering an empty webseeds list")
			d.webseeds = []string{}
		}
	})
	wg.Wait()
	for _, err := range []error{errProps, errTrackers, errFiles, errPieces, errPeers} {
		if err != nil {
			return nil, err
		}
	}
	return d, nil
}

// applyDetails maps the fetched answers into one torrent-get entry.
func applyDetails(dst JsonMap, t *qBT.TorrentInfo, plan detailPlan, d *detailData) {
	if plan.props {
		storeStaticProps(t.Hash, d.props)
		MapPropsGeneral(dst, d.props)
	}
	if plan.trackers {
		MapPropsTrackers(dst, d.trackers)
	}
	if plan.files {
		storeFileCount(t.Hash, d.files)
		MapPropsFiles(dst, d.files)
	}
	if plan.pieces {
		MapPieceStates(dst, d.pieces)
	}
	if plan.peers {
		MapPropsPeers(dst, d.peers)
	}
	if plan.webseeds {
		dst["webseeds"] = d.webseeds
	}
}

// --- files

// qBTFilePriorityToTransmission is Xmister's read mapping (plan.md Decisions):
// 1 (qBT "Normal", the default) → low, 7 (Maximum) → high, everything else
// (0 unwanted, 2–5 from old releases, 6 High) → normal. Unwanted is carried by
// `wanted`, not by the priority.
func qBTFilePriorityToTransmission(p int) int {
	switch p {
	case 1:
		return TR_PRI_LOW
	case 7:
		return TR_PRI_HIGH
	default:
		return TR_PRI_NORMAL
	}
}

// fileBytesCompleted turns qBT's per-file progress (a fraction) back into bytes.
// Rounding recovers the exact count; an incomplete file never reports full.
func fileBytesCompleted(size int64, progress float64) int64 {
	switch {
	case size <= 0 || progress <= 0:
		return 0
	case progress >= 1:
		return size
	}
	b := int64(math.Round(float64(size) * progress))
	if b >= size {
		b = size - 1
	}
	return b
}

// MapPropsFiles maps torrents/files. qBT's names are paths relative to the save
// path, including the torrent's top folder for multi-file torrents (the
// "Original" content layout), which is what Transmission's file names are too.
// begin_piece/end_piece (rpc 18) come from piece_range; Transmission's end is
// exclusive, qBT's last index inclusive.
func MapPropsFiles(dst JsonMap, filesInfo []qBT.PropertiesFiles) {
	fileNum := len(filesInfo)
	files := make([]JsonMap, fileNum)
	fileStats := make([]JsonMap, fileNum)
	priorities := make([]int, fileNum)
	wanted := make([]int, fileNum)
	for i, value := range filesInfo {
		bytesCompleted := fileBytesCompleted(value.Size, value.Progress)
		priority := qBTFilePriorityToTransmission(value.Priority)
		files[i] = JsonMap{
			"bytesCompleted": bytesCompleted,
			"length":         value.Size,
			"name":           EscapeString(value.Name),
		}
		if len(value.Piece_range) == 2 {
			begin, last := value.Piece_range[0], value.Piece_range[1]
			files[i]["begin_piece"] = begin
			files[i]["end_piece"] = maxInt(last+1, begin+1)
		}
		fileStats[i] = JsonMap{
			"bytesCompleted": bytesCompleted,
			"wanted":         value.Priority != 0,
			"priority":       priority,
		}
		priorities[i] = priority
		if value.Priority != 0 {
			wanted[i] = 1 // old bespoke API: 0/1, not booleans (rpc-spec.md)
		}
	}
	dst["files"] = files
	dst["fileStats"] = fileStats
	dst["priorities"] = priorities
	dst["wanted"] = wanted
	dst["file-count"] = fileNum
	dst["primary-mime-type"] = primaryMimeType(filesInfo)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

const fallbackMimeType = "application/octet-stream"

// commonMimeTypes are registered with the mime package so common torrent
// payloads get the same type everywhere (Go's built-in table is tiny and the
// system tables differ, e.g. none in a scratch image). Values from
// libtransmission/mime-types.h (4.1.4), mp4 as video/mp4 like Transmission.
var commonMimeTypes = map[string]string{
	".7z": "application/x-7z-compressed", ".aac": "audio/aac", ".apk": "application/vnd.android.package-archive",
	".avi": "video/x-msvideo", ".bz2": "application/x-bzip2", ".cbr": "application/x-cbr", ".cbz": "application/x-cbr",
	".deb": "application/x-debian-package", ".dmg": "application/x-apple-diskimage", ".epub": "application/epub+zip",
	".exe": "application/x-msdownload", ".flac": "audio/x-flac", ".flv": "video/x-flv", ".gz": "application/gzip",
	".iso": "application/x-iso9660-image", ".jpeg": "image/jpeg", ".jpg": "image/jpeg", ".m2ts": "video/mp2t",
	".m4a": "audio/mp4", ".m4v": "video/x-m4v", ".mka": "audio/x-matroska", ".mkv": "video/x-matroska",
	".mobi": "application/x-mobipocket-ebook", ".mov": "video/quicktime", ".mp3": "audio/mpeg", ".mp4": "video/mp4",
	".mpeg": "video/mpeg", ".mpg": "video/mpeg", ".nfo": "text/x-nfo", ".ogg": "audio/ogg", ".opus": "audio/ogg",
	".pdf": "application/pdf", ".png": "image/png", ".rar": "application/vnd.rar", ".srt": "application/x-subrip",
	".tar": "application/x-tar", ".torrent": "application/x-bittorrent", ".ts": "video/mp2t", ".txt": "text/plain",
	".vob": "video/x-ms-vob", ".vtt": "text/vtt", ".wav": "audio/x-wav", ".webm": "video/webm", ".wma": "audio/x-ms-wma",
	".wmv": "video/x-ms-wmv", ".xz": "application/x-xz", ".zip": "application/zip",
}

func init() {
	for ext, typ := range commonMimeTypes {
		_ = mime.AddExtensionType(ext, typ)
	}
}

// mimeTypeForFile: the file's MIME type by extension without parameters, or
// application/octet-stream (libtransmission tr_get_mime_type_for_filename).
func mimeTypeForFile(name string) string {
	ext := strings.ToLower(path.Ext(strings.ReplaceAll(name, "\\", "/")))
	if ext == "" || ext == "." {
		return fallbackMimeType
	}
	typ := mime.TypeByExtension(ext)
	if i := strings.IndexByte(typ, ';'); i >= 0 {
		typ = strings.TrimSpace(typ[:i])
	}
	switch typ {
	case "":
		return fallbackMimeType
	case "application/mp4":
		return "video/mp4"
	}
	return typ
}

// primaryMimeType: the MIME type with the most bytes over all files, like
// Transmission's tr_torrent_files::primary_mime_type (ties: alphabetical, so
// the answer is stable). No files → application/octet-stream.
func primaryMimeType(files []qBT.PropertiesFiles) string {
	sizes := map[string]int64{}
	for _, f := range files {
		sizes[mimeTypeForFile(f.Name)] += maxInt64(f.Size, 0)
	}
	best, bestSize := fallbackMimeType, int64(-1)
	for typ, size := range sizes {
		if size > bestSize || (size == bestSize && typ < best) {
			best, bestSize = typ, size
		}
	}
	return best
}

// --- pieces

// piecesBitfield: Transmission's `pieces`, one bit per piece set when we have it
// (qBT pieceStates 2; 1 = downloading counts as missing), most significant bit
// first, no header, base64 (libtransmission tr_bitfield, rpcimpl.cc
// make_piece_bitfield). h31/Reflection PR #6 did the same but sized the buffer
// len/8+1, one byte too many when the piece count is a multiple of 8.
func piecesBitfield(states []byte) string {
	bits := make([]byte, (len(states)+7)/8)
	for i, state := range states {
		if state == 2 {
			bits[i/8] |= 0x80 >> (uint(i) % 8)
		}
	}
	return base64.StdEncoding.EncodeToString(bits)
}

func MapPieceStates(dst JsonMap, pieces []byte) {
	dst["pieces"] = piecesBitfield(pieces)
}

// --- trackers

// scrapeURL derives the scrape URL like Transmission (announce-list.cc
// announce_to_scrape): "/announce" right after the last '/' becomes "/scrape";
// UDP trackers use the announce URL; otherwise there is none.
func scrapeURL(announce string) string {
	if pos := strings.LastIndexByte(announce, '/'); pos >= 0 && strings.HasPrefix(announce[pos:], "/announce") {
		return announce[:pos] + "/scrape" + announce[pos+len("/announce"):]
	}
	if strings.HasPrefix(announce, "udp:") {
		return announce
	}
	return ""
}

// Transmission tr_tracker_state (transmission.h).
const (
	TR_TRACKER_INACTIVE = 0
	TR_TRACKER_WAITING  = 1
	TR_TRACKER_ACTIVE   = 3
)

// MapPropsTrackers maps torrents/trackers (details requests) to trackers,
// trackerStats and trackerList, skipping the DHT/PeX/LSD pseudo-entries.
// Trackers keep qBT's order within a tier; tiers are sorted. IDs are the
// position in that order. qBT status (4.6.4 TrackerEntry::Status, 5.3.0
// TrackerEndpointState): 0 disabled (pseudo-entries only), 1 not contacted,
// 2 working, 3 updating (4.x; 5.x sends `updating` instead), 4 not working,
// 5 tracker error, 6 unreachable (5.x).
func MapPropsTrackers(dst JsonMap, trackers []qBT.PropertiesTrackers) {
	real := make([]qBT.PropertiesTrackers, 0, len(trackers))
	tiers := make([]int, 0, len(trackers))
	for _, tr := range trackers {
		tier, ok := trackerTier(tr.Tier)
		if strings.HasPrefix(tr.Url, "** [") || (!ok && tr.Tier != nil) {
			continue
		}
		real = append(real, tr)
		tiers = append(tiers, tier)
	}
	order := make([]int, len(real))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return tiers[order[a]] < tiers[order[b]] })

	trackersList := make([]JsonMap, 0, len(real))
	stats := make([]JsonMap, 0, len(real))
	var tierURLs [][]string
	for id, idx := range order {
		tr, tier := real[idx], tiers[idx]
		host, site := trackerHost(tr.Url)
		scrape := scrapeURL(tr.Url)
		trackersList = append(trackersList, JsonMap{
			"announce": tr.Url, "id": id, "scrape": scrape, "sitename": site, "tier": tier,
		})

		stat := transmission.TrackerStatsTemplate()
		stat["announce"] = tr.Url
		stat["scrape"] = scrape
		stat["host"] = host
		stat["sitename"] = site
		stat["id"] = id
		stat["tier"] = tier
		stat["seederCount"] = tr.Num_seeds
		stat["leecherCount"] = tr.Num_leeches
		stat["downloadCount"] = tr.Num_downloaded
		stat["lastAnnouncePeerCount"] = maxInt64(int64(tr.Num_peers), 0)
		stat["lastAnnounceResult"] = tr.Msg
		if tr.Next_announce > 0 {
			stat["nextAnnounceTime"] = tr.Next_announce
		}
		switch tr.Status {
		case 0:
			stat["announceState"] = TR_TRACKER_INACTIVE
		case 1, 3:
			stat["announceState"] = TR_TRACKER_WAITING
		case 2:
			stat["announceState"] = TR_TRACKER_WAITING
			stat["hasAnnounced"] = true
			stat["lastAnnounceSucceeded"] = true
			if tr.Msg == "" {
				stat["lastAnnounceResult"] = "Success"
			}
		default: // 4, 5, 6
			stat["announceState"] = TR_TRACKER_WAITING
			stat["hasAnnounced"] = true
			stat["lastAnnounceSucceeded"] = false
			if tr.Msg == "" {
				stat["lastAnnounceResult"] = "Tracker is not working"
			}
		}
		if tr.Status == 3 || tr.Updating {
			stat["announceState"] = TR_TRACKER_ACTIVE
		}
		stats = append(stats, JsonMap(stat))

		if id == 0 || tiers[order[id-1]] != tier {
			tierURLs = append(tierURLs, nil)
		}
		tierURLs[len(tierURLs)-1] = append(tierURLs[len(tierURLs)-1], tr.Url)
	}
	tierStrings := make([]string, len(tierURLs))
	for i, urls := range tierURLs {
		tierStrings[i] = strings.Join(urls, "\n")
	}
	dst["trackers"] = trackersList
	dst["trackerStats"] = stats
	dst["trackerList"] = strings.Join(tierStrings, "\n\n")
}

// --- peers

func getTorrentPeers(hash qBT.Hash) (map[string]qBT.PeerInfo, error) {
	data, err := qBTConn.Get("sync/torrentPeers", map[string]string{"hash": string(hash), "rid": "0"})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Peers map[string]qBT.PeerInfo
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("qBittorrent sync/torrentPeers: decoding response (%d bytes): %w", len(data), err)
	}
	return resp.Peers, nil
}

// peerFlags are qBT's peer flag letters (PeerInfo::determineFlags, same in
// 4.6.4 and 5.3.0, space-separated): D/d we're interested and unchoked/choked,
// U/u peer interested and unchoked/choked by us, K peer unchoked us but we're
// not interested, ? we unchoked the peer but it isn't interested, O optimistic
// unchoke, S snubbed, I incoming, H DHT, X PeX, L LSD, E RC4 encrypted,
// e encrypted handshake, P uTP, h holepunched (5.x).
type peerFlags map[string]bool

func parsePeerFlags(s string) peerFlags {
	f := peerFlags{}
	for _, tok := range strings.Fields(s) {
		f[tok] = true
	}
	return f
}

// mapPeer converts one qBT peer. The booleans are derived from the flags:
// clientIsChoked unless D or K (both mean the peer unchoked us); peerIsChoked
// unless U or ? (both mean we unchoked the peer). The peer counts in one
// peersFrom bucket, checked in the order incoming, DHT, PeX, LSD, else tracker
// (qBT doesn't flag tracker or resume-data peers). flagStr is rebuilt in
// Transmission's letters and order (peer-mgr.cc get_peer_stats: T O D/d U/u K
// ? E H/X I), so clients that parse it (TrguiNG checks T, E, I, H, X) work.
func mapPeer(p qBT.PeerInfo) (transmission.PeerInfo, string) {
	f := parsePeerFlags(p.Flags)
	tp := transmission.PeerInfo{
		Address:            p.IP,
		ClientName:         EscapeString(p.Client),
		ClientIsInterested: f["D"] || f["d"],
		ClientIsChoked:     !(f["D"] || f["K"]),
		PeerIsInterested:   f["U"] || f["u"],
		PeerIsChoked:       !(f["U"] || f["?"]),
		IsDownloadingFrom:  f["D"],
		IsUploadingTo:      f["U"],
		IsEncrypted:        f["E"] || f["e"],
		IsIncoming:         f["I"],
		IsUTP:              f["P"],
		Port:               p.Port,
		Progress:           p.Progress,
		RateToClient:       p.Dl_speed,
		RateToPeer:         p.Up_speed,
	}
	if tp.Address == "" && p.I2p_dest != "" {
		tp.Address = p.I2p_dest
	}
	from := "fromTracker"
	switch {
	case f["I"]:
		from = "fromIncoming"
	case f["H"]:
		from = "fromDht"
	case f["X"]:
		from = "fromPex"
	case f["L"]:
		from = "fromLpd"
	}

	var b strings.Builder
	write := func(cond bool, s string) {
		if cond {
			b.WriteString(s)
		}
	}
	write(tp.IsUTP, "T")
	write(f["O"], "O")
	write(tp.IsDownloadingFrom, "D")
	write(!tp.IsDownloadingFrom && tp.ClientIsInterested, "d")
	write(tp.IsUploadingTo, "U")
	write(!tp.IsUploadingTo && tp.PeerIsInterested, "u")
	write(!tp.ClientIsChoked && !tp.ClientIsInterested, "K")
	write(!tp.PeerIsChoked && !tp.PeerIsInterested, "?")
	write(tp.IsEncrypted, "E")
	write(from == "fromDht", "H")
	write(from == "fromPex", "X")
	write(tp.IsIncoming, "I")
	tp.FlagStr = b.String()
	return tp, from
}

// MapPropsPeers maps sync/torrentPeers to peers (sorted by qBT's "ip:port" key,
// so the order is stable) and peersFrom.
func MapPropsPeers(dst JsonMap, peers map[string]qBT.PeerInfo) {
	keys := make([]string, 0, len(peers))
	for k := range peers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	trPeers := make([]transmission.PeerInfo, 0, len(peers))
	from := transmission.PeersFromTemplate()
	for _, k := range keys {
		tp, bucket := mapPeer(peers[k])
		trPeers = append(trPeers, tp)
		from[bucket] = from[bucket].(int) + 1
	}
	dst["peers"] = trPeers
	dst["peersFrom"] = from
}

// --- webseeds

func getWebseeds(hash qBT.Hash) ([]string, error) {
	data, err := qBTConn.Get("torrents/webseeds", map[string]string{"hash": string(hash)})
	if err != nil {
		return nil, err
	}
	var seeds []struct{ Url string }
	if err := json.Unmarshal(data, &seeds); err != nil {
		return nil, fmt.Errorf("qBittorrent torrents/webseeds: decoding response (%d bytes): %w", len(data), err)
	}
	urls := make([]string, 0, len(seeds))
	for _, s := range seeds {
		urls = append(urls, s.Url)
	}
	return urls, nil
}
