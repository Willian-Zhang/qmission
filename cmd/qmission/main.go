package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/Willian-Zhang/qmission/qBT"
	"github.com/Willian-Zhang/qmission/transmission"
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

type argumentValue int

const (
	ARGUMENT_NOT_SET argumentValue = iota
	ARGUMENT_TRUE    argumentValue = iota
	ARGUMENT_FALSE   argumentValue = iota
)

type additionalArguments struct {
	sequentialDownload   argumentValue
	firstLastPiecesFirst argumentValue
	skipChecking         argumentValue
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

func TorrentStop(args json.RawMessage) (JsonMap, error) {
	torrents, err := parseActionArgument(args)
	if err != nil {
		return nil, err
	}
	log.WithField("count", len(torrents)).Debug("Stopping torrents")
	return JsonMap{}, qBTConn.StopTorrents(torrents.Hashes())
}

func TorrentStart(args json.RawMessage) (JsonMap, error) {
	torrents, err := parseActionArgument(args)
	if err != nil {
		return nil, err
	}
	log.WithField("count", len(torrents)).Debug("Starting torrents")
	return JsonMap{}, qBTConn.StartTorrents(torrents.Hashes())
}

func TorrentRecheck(args json.RawMessage) (JsonMap, error) {
	torrents, err := parseActionArgument(args)
	if err != nil {
		return nil, err
	}
	log.WithField("count", len(torrents)).Debug("Verifying torrents")
	return JsonMap{}, qBTConn.RecheckTorrents(torrents.Hashes())
}

func TorrentDelete(args json.RawMessage) (JsonMap, error) {
	var req struct {
		Ids             *json.RawMessage
		DeleteLocalData interface{} `json:"delete-local-data"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	sel, err := parseIDsField(req.Ids)
	if err != nil {
		return nil, err
	}
	deleteFiles := parseDeleteFilesField(req.DeleteLocalData)
	log.WithField("count", len(sel.torrents)).WithField("deleteFiles", deleteFiles).Info("Removing torrents")
	log.WithField("hashes", sel.torrents.ConcatenateHashes()).Debug("Removing torrents")
	return JsonMap{}, qBTConn.DeleteTorrents(sel.torrents.Hashes(), deleteFiles)
}

func parseDeleteFilesField(deleteLocalData interface{}) bool {
	switch val := deleteLocalData.(type) {
	case bool:
		return val
	case float64:
		return val != 0
	default:
		return false // Default value
	}
}

func PutMIMEField(mime *multipart.Writer, fieldName string, value string) error {
	w, err := mime.CreateFormField(fieldName)
	if err != nil {
		return err
	}
	_, err = w.Write([]byte(value))
	return err
}

func AdditionalArgumentToString(value argumentValue) string {
	switch value {
	case ARGUMENT_TRUE:
		return "true"
	case ARGUMENT_FALSE:
		return "false"
	default:
		return ""
	}
}

func UploadTorrent(metainfo []byte, urls *string, req *transmission.TorrentAddRequest, paused bool) error {
	var buffer bytes.Buffer
	mime := multipart.NewWriter(&buffer)

	if metainfo != nil {
		mimeWriter, err := mime.CreateFormFile("torrents", "example.torrent")
		if err != nil {
			return err
		}
		if _, err := mimeWriter.Write(metainfo); err != nil {
			return err
		}
	}

	fields := [][2]string{}
	if urls != nil {
		fields = append(fields, [2]string{"urls", *urls})
	}
	if req.Download_dir != nil {
		extraArgs, strippedLocation, err := parseAdditionalLocationArguments(*req.Download_dir)
		if err != nil {
			return err
		}
		if extraArgs.sequentialDownload != ARGUMENT_NOT_SET {
			fields = append(fields, [2]string{"sequentialDownload", AdditionalArgumentToString(extraArgs.sequentialDownload)})
		}
		if extraArgs.firstLastPiecesFirst != ARGUMENT_NOT_SET {
			fields = append(fields, [2]string{"firstLastPiecePrio", AdditionalArgumentToString(extraArgs.firstLastPiecesFirst)})
		}
		if extraArgs.skipChecking != ARGUMENT_NOT_SET {
			fields = append(fields, [2]string{"skip_checking", AdditionalArgumentToString(extraArgs.skipChecking)})
		}
		fields = append(fields, [2]string{"savepath", strippedLocation})
	}
	fields = append(fields, [2]string{qBTConn.Gates().AddStoppedParam, strconv.FormatBool(paused)})
	for _, f := range fields {
		if err := PutMIMEField(mime, f[0], f[1]); err != nil {
			return err
		}
	}
	if err := mime.Close(); err != nil {
		return err
	}

	if _, err := qBTConn.PostMultipart("torrents/add", mime.FormDataContentType(), &buffer); err != nil {
		return err
	}
	log.Debug("Torrent uploaded")
	return nil
}

func ParseMagnetLink(link string) (newHash qBT.Hash, newName string, err error) {
	params, err := url.ParseQuery(strings.TrimPrefix(link, "magnet:?"))
	if err != nil {
		return "", "", fmt.Errorf("invalid magnet link: %w", err)
	}
	xt := params.Get("xt")
	if !strings.HasPrefix(xt, "urn:btih:") {
		return "", "", errors.New("invalid magnet link: no urn:btih info hash")
	}
	newHash = qBT.Hash(strings.ToLower(strings.TrimPrefix(xt, "urn:btih:")))
	newName = params.Get("dn")
	if newName == "" {
		newName = "Torrent name"
	}
	return newHash, newName, nil
}

func ParseMetainfo(metainfo []byte) (newHash qBT.Hash, newName string, err error) {
	var parsedMetaInfo MetaInfo
	if !parsedMetaInfo.ReadTorrentMetaInfoFile(bytes.NewBuffer(metainfo)) {
		return "", "", errors.New("invalid torrent metainfo")
	}
	log.WithFields(log.Fields{
		"len":  len(metainfo),
		"sha1": fmt.Sprintf("%x", sha1.Sum(metainfo)),
	}).Debug("Decoded metainfo")
	return qBT.Hash(fmt.Sprintf("%x", parsedMetaInfo.InfoHash)), parsedMetaInfo.Info.Name, nil
}

func TorrentAdd(args json.RawMessage) (JsonMap, error) {
	var req transmission.TorrentAddRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if err := qBTConn.UpdateTorrentsList(); err != nil {
		return nil, err
	}

	paused := false
	if req.Paused != nil {
		switch value := (*req.Paused).(type) {
		case float64: // Transmission Remote GUI sends a number
			paused = value != 0
		case bool:
			paused = value
		}
	}

	var newHash qBT.Hash
	var newName string
	var err error
	switch {
	case req.Metainfo != nil:
		metainfo, decErr := base64.StdEncoding.DecodeString(*req.Metainfo)
		if decErr != nil {
			return nil, fmt.Errorf("invalid metainfo: %w", decErr)
		}
		if newHash, newName, err = ParseMetainfo(metainfo); err != nil {
			return nil, err
		}
		err = UploadTorrent(metainfo, nil, &req, paused)
	case req.Filename != nil && strings.HasPrefix(*req.Filename, "magnet:?"):
		if newHash, newName, err = ParseMagnetLink(*req.Filename); err != nil {
			return nil, err
		}
		err = UploadTorrent(nil, req.Filename, &req, paused)
	case req.Filename != nil && strings.HasPrefix(*req.Filename, "http"):
		metainfo, getErr := DoGetWithCookies(*req.Filename, req.Cookies)
		if getErr != nil {
			return nil, getErr
		}
		if newHash, newName, err = ParseMetainfo(metainfo); err != nil {
			return nil, err
		}
		err = UploadTorrent(metainfo, nil, &req, paused)
	default:
		return nil, errors.New("torrent-add: no filename or metainfo given")
	}
	if err != nil {
		return nil, err
	}

	if torrent := qBTConn.TorrentsList.ByHash(newHash); torrent != nil {
		return JsonMap{
			"torrent-duplicate": JsonMap{"id": torrent.Id, "name": newName, "hashString": newHash},
		}, nil
	}

	var torrent *qBT.TorrentInfo
	for retries := 0; retries < 100; retries++ {
		time.Sleep(50 * time.Millisecond)
		if err := qBTConn.UpdateTorrentsList(); err != nil {
			return nil, err
		}
		if torrent = qBTConn.TorrentsList.ByHash(newHash); torrent != nil {
			break
		}
	}
	if torrent == nil {
		return nil, errors.New("torrent-add: qBittorrent accepted the torrent but it did not show up in time")
	}
	log.WithField("id", torrent.Id).Debug("New torrent")
	return JsonMap{
		"torrent-added": JsonMap{"id": torrent.Id, "name": newName, "hashString": newHash},
	}, nil
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

var additionalArgumentsRegexp = regexp.MustCompile("([+\\-])([sfh]+)$")

func parseAdditionalLocationArguments(originalLocation string) (args additionalArguments, strippedLocation string, err error) {
	strippedLocation = additionalArgumentsRegexp.ReplaceAllLiteralString(originalLocation, "")
	submatches := additionalArgumentsRegexp.FindStringSubmatch(originalLocation)
	if len(submatches) == 0 {
		return
	}
	for _, c := range submatches[2] {
		flagValue := ARGUMENT_NOT_SET
		switch submatches[1] {
		case "+":
			flagValue = ARGUMENT_TRUE
		case "-":
			flagValue = ARGUMENT_FALSE
		default:
			err = errors.New("Unknown value: " + submatches[1])
			return
		}
		switch c {
		case 's':
			args.sequentialDownload = flagValue
		case 'f':
			args.firstLastPiecesFirst = flagValue
		case 'h':
			args.skipChecking = flagValue
		default:
			err = errors.New("Unknown value: " + submatches[1])
			return
		}
	}
	return
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

	extraArgs, strippedLocation, err := parseAdditionalLocationArguments(*req.Location)
	if err != nil {
		return nil, err
	}
	if extraArgs.firstLastPiecesFirst != ARGUMENT_NOT_SET {
		for _, torrent := range torrents {
			if err := qBTConn.SetFirstLastPieceFirst(torrent.Hash, extraArgs.firstLastPiecesFirst == ARGUMENT_TRUE); err != nil {
				return nil, err
			}
		}
	}
	if extraArgs.sequentialDownload != ARGUMENT_NOT_SET {
		for _, torrent := range torrents {
			if err := qBTConn.SetSequentialDownload(torrent.Hash, extraArgs.sequentialDownload == ARGUMENT_TRUE); err != nil {
				return nil, err
			}
		}
	}

	params := url.Values{
		"hashes":   {torrents.ConcatenateHashes()},
		"location": {strippedLocation},
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
