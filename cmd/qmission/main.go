package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"

	"net/http"
	"net/url"
	"os"

	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/Willian-Zhang/qmission/qBT"
	log "github.com/sirupsen/logrus"
)

var (
	verbose          = flag.Bool("verbose", false, "Enable verbose output")
	debug            = flag.Bool("debug", false, "Enable debug output (logs RPC bodies, which contain torrent data)")
	apiAddr          = flag.String("api-addr", "http://localhost:8080/", "qBittorrent API address")
	port             = flag.Uint("port", 9091, "Transmission RPC port")
	cacheTimeout     = flag.Uint("cache-timeout", 15, "Deprecated, has no effect: list polls no longer call per-torrent endpoints")
	disableKeepAlive = flag.Bool("disable-keep-alive", false, "Disable HTTP Keep-Alive in requests (may be necessary for older qBittorrent versions)")
	useSync          = flag.Bool("sync", true, "Use Sync endpoint (recommended)")
	qbtUserFlag      = flag.String("qbt-user", "", "qBittorrent WebUI username (default: $QBT_USER)")
	qbtPassFlag      = flag.String("qbt-pass", "", "qBittorrent WebUI password (default: $QBT_PASS; prefer the env var, flags are visible in ps)")
)

func init() {
	flag.BoolVar(verbose, "v", false, "")
	flag.BoolVar(debug, "d", false, "")
	flag.StringVar(apiAddr, "r", "http://localhost:8080/", "")
	flag.UintVar(port, "p", 9091, "")
}

var qBTConn qBT.Connection

// idSelection is the result of parsing a Transmission `ids` argument.
type idSelection struct {
	torrents       qBT.TorrentInfoList
	explicit       bool // ids was given (not "all torrents")
	recentlyActive bool
}

// parseIDsField refreshes the torrent list and resolves `ids` (rpc-spec 3.1):
// absent = all torrents, a number, "recently-active", a hash string, or a list
// of numbers and hashes. Unknown or removed IDs/hashes are skipped, as
// Transmission does.
func parseIDsField(args *json.RawMessage) (idSelection, error) {
	if err := qBTConn.UpdateTorrentsList(); err != nil {
		return idSelection{}, err
	}

	if args == nil || len(*args) == 0 || string(*args) == "null" {
		return idSelection{torrents: qBTConn.TorrentsList.Slice()}, nil
	}

	var ids interface{}
	if err := json.Unmarshal(*args, &ids); err != nil {
		return idSelection{}, fmt.Errorf("invalid ids: %w", err)
	}

	sel := idSelection{explicit: true, torrents: qBT.TorrentInfoList{}}
	seen := map[qBT.Hash]bool{}
	add := func(t *qBT.TorrentInfo) {
		if t != nil && !seen[t.Hash] {
			seen[t.Hash] = true
			sel.torrents = append(sel.torrents, t)
		}
	}
	addOne := func(value interface{}) error {
		switch id := value.(type) {
		case float64:
			if id != float64(int64(id)) {
				return fmt.Errorf("invalid torrent id %v", id)
			}
			add(qBTConn.TorrentsList.ByID(qBT.ID(id)))
		case string:
			if id == "recently-active" || id == "recently_active" {
				sel.recentlyActive = true
				if *useSync {
					for _, t := range qBTConn.TorrentsList.GetActive() {
						add(t)
					}
				} else {
					for _, t := range qBTConn.TorrentsList.Slice() {
						add(t)
					}
				}
			} else {
				add(qBTConn.TorrentsList.ByHash(qBT.Hash(strings.ToLower(id))))
			}
		default:
			return fmt.Errorf("invalid ids element of type %T", value)
		}
		return nil
	}

	switch ids := ids.(type) {
	case []interface{}:
		for _, value := range ids {
			if err := addOne(value); err != nil {
				return idSelection{}, err
			}
		}
	default:
		if err := addOne(ids); err != nil {
			return idSelection{}, err
		}
	}
	return sel, nil
}

func parseActionArgument(args json.RawMessage) (qBT.TorrentInfoList, error) {
	var req struct {
		Ids *json.RawMessage
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	sel, err := parseIDsField(req.Ids)
	return sel.torrents, err
}

func isMn(r rune) bool {
	return unicode.Is(unicode.Mn, r) // Mn: nonspacing marks
}

type escapedString string

// MarshalJSON writes a JSON string with every non-ASCII rune as \uXXXX (surrogate
// pairs above the BMP). strconv.QuoteToASCII can't be used: its \U and \x escapes
// aren't valid JSON.
func (s escapedString) MarshalJSON() ([]byte, error) {
	quoted, err := json.Marshal(string(s)) // escapes quotes/controls, invalid UTF-8 -> U+FFFD
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.Grow(len(quoted))
	for _, r := range string(quoted) {
		switch {
		case r < utf8.RuneSelf:
			b.WriteRune(r)
		case r > 0xFFFF:
			hi, lo := utf16.EncodeRune(r)
			fmt.Fprintf(&b, `\u%04x\u%04x`, hi, lo)
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	return []byte(b.String()), nil
}

func EscapeString(in string) escapedString {
	return escapedString(in)
}

// TorrentSet supports only files-wanted / files-unwanted so far; any other
// key fails the request (stage 2 implements the diff rule from plan.md).
func TorrentSet(args json.RawMessage) (JsonMap, error) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(args, &keys); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	for key := range keys {
		if key != "ids" && key != "files-wanted" && key != "files-unwanted" {
			log.WithField("field", key).Warn("torrent-set: field not supported yet")
			return nil, errNotImplemented
		}
	}

	var req struct {
		Ids            *json.RawMessage
		Files_wanted   *[]int `json:"files-wanted"`
		Files_unwanted *[]int `json:"files-unwanted"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if req.Files_wanted == nil && req.Files_unwanted == nil {
		return JsonMap{}, nil
	}

	sel, err := parseIDsField(req.Ids)
	if err != nil {
		return nil, err
	}
	if len(sel.torrents) != 1 {
		return nil, errors.New("torrent-set: files-wanted/files-unwanted need exactly one torrent")
	}
	torrent := sel.torrents[0]

	newFilesPriorities := make(map[int]int)
	if req.Files_wanted != nil {
		for _, fileId := range *req.Files_wanted {
			newFilesPriorities[fileId] = 1 // qBT Normal
		}
	}
	if req.Files_unwanted != nil {
		for _, fileId := range *req.Files_unwanted {
			newFilesPriorities[fileId] = 0 // Do not download
		}
	}
	for fileId, priority := range newFilesPriorities {
		params := url.Values{
			"hash":     {string(torrent.Hash)},
			"id":       {strconv.Itoa(fileId)},
			"priority": {strconv.Itoa(priority)},
		}
		if _, err := qBTConn.PostForm("torrents/filePrio", params); err != nil {
			return nil, err
		}
	}
	return JsonMap{}, nil
}

func TorrentSetLocation(args json.RawMessage) (JsonMap, error) {
	var req struct {
		Ids      *json.RawMessage
		Location *string     `json:"location"`
		Move     interface{} `json:"move"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if req.Location == nil {
		return nil, errors.New("torrent-set-location: no location given")
	}

	sel, err := parseIDsField(req.Ids)
	if err != nil {
		return nil, err
	}
	torrents := sel.torrents
	if len(torrents) == 0 {
		return JsonMap{}, nil
	}

	params := url.Values{
		"hashes":   {torrents.ConcatenateHashes()},
		"location": {*req.Location},
	}
	if _, err := qBTConn.PostForm("torrents/setLocation", params); err != nil {
		return nil, err
	}
	return JsonMap{}, nil
}

// config is the startup configuration taken from flags and the environment.
type config struct {
	qbtUser, qbtPass       string
	bridgeUser, bridgePass string
}

var errNoBridgeCredentials = errors.New("bridge credentials required (QMISSION_USER/QMISSION_PASS); passthrough auth not implemented yet")

func loadConfig(getenv func(string) string) (config, error) {
	c := config{
		qbtUser:    getenv("QBT_USER"),
		qbtPass:    getenv("QBT_PASS"),
		bridgeUser: getenv("QMISSION_USER"),
		bridgePass: getenv("QMISSION_PASS"),
	}
	if *qbtUserFlag != "" {
		c.qbtUser = *qbtUserFlag
	}
	if *qbtPassFlag != "" {
		c.qbtPass = *qbtPassFlag
		log.Warn("-qbt-pass is visible in the process list; prefer the QBT_PASS environment variable")
	}
	if c.bridgeUser == "" || c.bridgePass == "" {
		return c, errNoBridgeCredentials
	}
	return c, nil
}

// Delay between background login attempts: doubles from loginRetryInitial up to loginRetryMax.
var (
	loginRetryInitial = 2 * time.Second
	loginRetryMax     = 60 * time.Second
)

// loginLoop logs in to qBT in the background and keeps retrying while qBT is
// unreachable, so the bridge starts (and survives) without a running qBT.
// Requests also log in on demand, so this only makes the first one faster and
// puts problems in the log early.
func loginLoop() {
	delay := loginRetryInitial
	for {
		err := qBTConn.Login()
		switch {
		case err == nil:
			log.WithField("version", qBTConn.AppVersion()).WithField("webapi", qBTConn.APIVersion().String()).
				Info("Connected to qBittorrent")
			cleanupLeftoverAddMarkers()
			return
		case errors.Is(err, qBT.ErrInvalidCredentials):
			log.WithError(err).Error("qBittorrent login failed; check QBT_USER/QBT_PASS and qBittorrent's Host-header validation. Not retrying")
			return
		case errors.Is(err, qBT.ErrUnsupportedVersion):
			log.WithError(err).Error("qBittorrent login failed. Not retrying")
			return
		}
		log.WithError(err).Errorf("qBittorrent login failed, retrying in %s", delay)
		time.Sleep(delay)
		if delay *= 2; delay > loginRetryMax {
			delay = loginRetryMax
		}
	}
}

func main() {
	flag.Parse()
	switch {
	case *debug:
		log.SetLevel(log.DebugLevel)
	case *verbose:
		log.SetLevel(log.InfoLevel)
	default:
		log.SetLevel(log.WarnLevel)
	}

	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	if cfg.qbtUser == "" {
		log.Warn("No qBittorrent username (QBT_USER); only works if qBittorrent skips auth for this host")
	}
	bridgeUser, bridgePass = cfg.bridgeUser, cfg.bridgePass

	tr := http.DefaultTransport.(*http.Transport).Clone()
	if *disableKeepAlive {
		log.Info("Disabled HTTP keep-alive")
		tr.DisableKeepAlives = true
	}
	cl := &http.Client{Transport: tr, Timeout: 60 * time.Second}
	if err := qBTConn.Init(*apiAddr, cl, *useSync); err != nil {
		log.Fatal(err)
	}
	qBTConn.SetCredentials(cfg.qbtUser, cfg.qbtPass)
	go loginLoop()

	addr := fmt.Sprintf(":%d", *port)
	log.WithField("addr", addr).Info("Listening")
	log.Fatal(http.ListenAndServe(addr, newMux()))
}
