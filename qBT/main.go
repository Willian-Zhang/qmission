package qBT

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

type Hash string

type ID int

const (
	// INVALID_ID marks a torrent without an ID. Assigned IDs start at 1.
	INVALID_ID ID = -1
)

var RECENTLY_ACTIVE_TIMEOUT = 60 * time.Second

// BannedRetryDelay is how long Login waits after a ban answer before asking qBT again.
var BannedRetryDelay = 60 * time.Second

// Connection is the one shared qBT client. All methods are safe for concurrent use.
type Connection struct {
	addr   *url.URL
	client *http.Client

	authMu     sync.RWMutex // guards everything below up to TorrentsList
	username   string
	password   string
	cookies    []*http.Cookie
	loggedIn   bool
	loginGen   uint64 // bumped on every successful login
	loginErr   error  // sticky ErrInvalidCredentials, or ErrBanned until bannedAt+BannedRetryDelay
	bannedAt   time.Time
	apiVersion APIVersion
	appVersion string

	updateMu     sync.Mutex // serialises UpdateTorrentsList (rid)
	TorrentsList TorrentsList
}

// Init resets the connection. baseUrl is qBT's WebUI root; the API lives under api/v2/.
func (q *Connection) Init(baseUrl string, client *http.Client, useSync bool) error {
	q.updateMu.Lock()
	defer q.updateMu.Unlock()
	q.TorrentsList.reset(useSync)

	q.authMu.Lock()
	defer q.authMu.Unlock()
	q.cookies = nil
	q.loggedIn = false
	q.loginErr = nil
	q.apiVersion = APIVersion{}
	q.appVersion = ""
	q.client = client

	parsedBaseAddr, err := url.Parse(baseUrl)
	if err != nil {
		return fmt.Errorf("invalid qBittorrent address %q: %w", baseUrl, err)
	}
	q.addr = parsedBaseAddr.ResolveReference(&url.URL{Path: "api/v2/"})
	return nil
}

// SetCredentials stores the qBT WebUI credentials used by Login and every re-login.
// Changing them clears a sticky ErrInvalidCredentials.
func (q *Connection) SetCredentials(username, password string) {
	q.authMu.Lock()
	defer q.authMu.Unlock()
	if username != q.username || password != q.password {
		q.username, q.password = username, password
		if errors.Is(q.loginErr, ErrInvalidCredentials) {
			q.loginErr = nil
		}
	}
}

func (q *Connection) IsLoggedIn() bool {
	q.authMu.RLock()
	defer q.authMu.RUnlock()
	return q.loggedIn
}

// APIVersion is the WebAPI version detected at the last login (zero before).
func (q *Connection) APIVersion() APIVersion {
	q.authMu.RLock()
	defer q.authMu.RUnlock()
	return q.apiVersion
}

// AppVersion is app/version from the last login, e.g. "v5.3.0".
func (q *Connection) AppVersion() string {
	q.authMu.RLock()
	defer q.authMu.RUnlock()
	return q.appVersion
}

// Gates returns the version-dependent names for the detected WebAPI version.
func (q *Connection) Gates() Gates {
	return GatesFor(q.APIVersion())
}

// Login logs in with the stored credentials and detects the qBT version.
func (q *Connection) Login() error {
	q.authMu.Lock()
	defer q.authMu.Unlock()
	return q.loginLocked()
}

// relogin logs in again unless another goroutine already did since gen.
func (q *Connection) relogin(gen uint64) error {
	q.authMu.Lock()
	defer q.authMu.Unlock()
	if q.loggedIn && q.loginGen != gen {
		return nil
	}
	return q.loginLocked()
}

func (q *Connection) ensureLoggedIn() (uint64, error) {
	q.authMu.RLock()
	loggedIn, gen := q.loggedIn, q.loginGen
	q.authMu.RUnlock()
	if loggedIn {
		return gen, nil
	}
	q.authMu.Lock()
	defer q.authMu.Unlock()
	if !q.loggedIn {
		if err := q.loginLocked(); err != nil {
			return 0, err
		}
	}
	return q.loginGen, nil
}

func (q *Connection) loginLocked() error {
	if q.addr == nil {
		return errors.New("qBT connection not initialised")
	}
	if q.loginErr != nil {
		if errors.Is(q.loginErr, ErrInvalidCredentials) || time.Since(q.bannedAt) < BannedRetryDelay {
			return q.loginErr
		}
	}
	q.loggedIn = false
	q.cookies = nil

	form := url.Values{"username": {q.username}, "password": {q.password}}
	req, err := http.NewRequest("POST", q.url("auth/login", nil), strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := q.client.Do(req)
	if err != nil {
		return fmt.Errorf("qBittorrent auth/login: %w", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return fmt.Errorf("qBittorrent auth/login: %w", err)
	}

	switch {
	case resp.StatusCode == http.StatusForbidden:
		q.loginErr, q.bannedAt = ErrBanned, time.Now()
		return ErrBanned
	case resp.StatusCode == http.StatusUnauthorized:
		// WebAPI >= 2.14 answers bad credentials with 401; any version also does
		// on a failed Host-header or CSRF check.
		q.loginErr = fmt.Errorf("%w (HTTP 401; or the Host header/CSRF check failed)", ErrInvalidCredentials)
		return q.loginErr
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%w: auth/login not found", ErrUnsupportedVersion)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return newHTTPError("POST", "auth/login", resp.StatusCode, body)
	case strings.TrimSpace(string(body)) == "Fails.":
		q.loginErr = ErrInvalidCredentials
		return ErrInvalidCredentials
	}

	// The session cookie is "SID" by default (4.x: configurable via
	// WebAPI/SessionCookieName; 5.x: "QBT_SID_<port>"), so keep every cookie.
	for _, c := range resp.Cookies() {
		if c != nil && c.Value != "" {
			q.cookies = append(q.cookies, c)
		}
	}
	q.loginErr = nil

	if err := q.detectVersionLocked(); err != nil {
		q.cookies = nil
		return err
	}
	q.loggedIn = true
	q.loginGen++
	log.WithField("webapi", q.apiVersion.String()).WithField("version", q.appVersion).Info("Logged in to qBittorrent")
	return nil
}

func (q *Connection) detectVersionLocked() error {
	status, body, err := q.send("GET", "app/webapiVersion", nil, "", nil, q.cookies)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return fmt.Errorf("%w: app/webapiVersion not found", ErrUnsupportedVersion)
	}
	if status != http.StatusOK {
		return newHTTPError("GET", "app/webapiVersion", status, body)
	}
	v, err := ParseAPIVersion(string(body))
	if err != nil {
		return err
	}
	if !v.AtLeast(MinAPIVersion) {
		return fmt.Errorf("%w: WebAPI %s", ErrUnsupportedVersion, v)
	}

	status, appBody, err := q.send("GET", "app/version", nil, "", nil, q.cookies)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return newHTTPError("GET", "app/version", status, appBody)
	}
	q.apiVersion = v
	q.appVersion = strings.TrimSpace(string(appBody))
	return nil
}

func (q *Connection) url(path string, params map[string]string) string {
	u := q.addr.ResolveReference(&url.URL{Path: strings.TrimPrefix(path, "/")})
	if len(params) > 0 {
		query := u.Query()
		for key, value := range params {
			query.Set(key, value)
		}
		u.RawQuery = query.Encode()
	}
	return u.String()
}

func (q *Connection) send(method, path string, params map[string]string, contentType string, body []byte, cookies []*http.Cookie) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, q.url(path, params), reader)
	if err != nil {
		return 0, nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for _, c := range cookies {
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	resp, err := q.client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("qBittorrent %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, fmt.Errorf("qBittorrent %s %s: %w", method, path, err)
	}
	// qBT 5.x may re-send the session cookie to extend its expiry; the ID stays
	// the same and we send the cookie by hand, so there is nothing to update.
	return resp.StatusCode, data, nil
}

// do sends an API request, logging in first if needed. On 403 it logs in once
// more and retries once.
func (q *Connection) do(method, path string, params map[string]string, contentType string, body []byte) ([]byte, error) {
	gen, err := q.ensureLoggedIn()
	if err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		q.authMu.RLock()
		cookies := q.cookies
		q.authMu.RUnlock()

		status, data, err := q.send(method, path, params, contentType, body, cookies)
		if err != nil {
			return nil, err
		}
		if status == http.StatusForbidden {
			if attempt > 0 {
				return nil, fmt.Errorf("%w: %s %s", ErrForbidden, method, path)
			}
			log.WithField("path", path).Info("qBittorrent answered 403, logging in again")
			if err := q.relogin(gen); err != nil {
				return nil, err
			}
			q.authMu.RLock()
			gen = q.loginGen
			q.authMu.RUnlock()
			continue
		}
		if status < 200 || status > 299 {
			return nil, newHTTPError(method, path, status, data)
		}
		return data, nil
	}
}

// Get calls a qBT API path (relative to api/v2/, e.g. "torrents/info") with GET.
func (q *Connection) Get(path string, params map[string]string) ([]byte, error) {
	return q.do("GET", path, params, "", nil)
}

// PostForm calls a qBT API path with a urlencoded POST body.
func (q *Connection) PostForm(path string, data url.Values) ([]byte, error) {
	return q.do("POST", path, nil, "application/x-www-form-urlencoded", []byte(data.Encode()))
}

// PostMultipart calls a qBT API path with a multipart body (torrents/add).
func (q *Connection) PostMultipart(path string, contentType string, body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	return q.do("POST", path, nil, contentType, data)
}

func (q *Connection) getJSON(path string, params map[string]string, dst interface{}) error {
	data, err := q.Get(path, params)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return decodeError(path, data, err)
	}
	return nil
}

func (q *Connection) PostWithHashes(path string, hashes []Hash) error {
	_, err := q.PostForm(path, url.Values{"hashes": {joinHashes(hashes)}})
	return err
}

// StopTorrents uses torrents/stop on WebAPI >= 2.11, torrents/pause before.
func (q *Connection) StopTorrents(hashes []Hash) error {
	if _, err := q.ensureLoggedIn(); err != nil {
		return err
	}
	return q.PostWithHashes(q.Gates().StopEndpoint, hashes)
}

// StartTorrents uses torrents/start on WebAPI >= 2.11, torrents/resume before.
func (q *Connection) StartTorrents(hashes []Hash) error {
	if _, err := q.ensureLoggedIn(); err != nil {
		return err
	}
	return q.PostWithHashes(q.Gates().StartEndpoint, hashes)
}

func (q *Connection) RecheckTorrents(hashes []Hash) error {
	return q.PostWithHashes("torrents/recheck", hashes)
}

// DeleteTorrents calls torrents/delete (POST-only in qBT 4.6 and 5.x).
func (q *Connection) DeleteTorrents(hashes []Hash, deleteFiles bool) error {
	_, err := q.PostForm("torrents/delete", url.Values{
		"hashes":      {joinHashes(hashes)},
		"deleteFiles": {strconv.FormatBool(deleteFiles)},
	})
	return err
}

func (q *Connection) AddNewCategory(category string) error {
	_, err := q.PostForm("torrents/createCategory", url.Values{"category": {category}})
	return err
}

func (q *Connection) GetPropsGeneral(hash Hash) (propGeneral PropertiesGeneral, err error) {
	err = q.getJSON("torrents/properties", map[string]string{"hash": string(hash)}, &propGeneral)
	return
}

func (q *Connection) GetPropsTrackers(hash Hash) (trackers []PropertiesTrackers, err error) {
	err = q.getJSON("torrents/trackers", map[string]string{"hash": string(hash)}, &trackers)
	return
}

func (q *Connection) GetPiecesStates(hash Hash) (pieces []byte, err error) {
	err = q.getJSON("torrents/pieceStates", map[string]string{"hash": string(hash)}, &pieces)
	return
}

func (q *Connection) GetPropsFiles(hash Hash) (files []PropertiesFiles, err error) {
	err = q.getJSON("torrents/files", map[string]string{"hash": string(hash)}, &files)
	return
}

func (q *Connection) GetPreferences() (pref Preferences, err error) {
	err = q.getJSON("app/preferences", nil, &pref)
	return
}

func (q *Connection) GetTransferInfo() (info TransferInfo, err error) {
	err = q.getJSON("transfer/info", nil, &info)
	return
}

// GetVersion returns app/version as detected at login (e.g. "v5.3.0").
func (q *Connection) GetVersion() (string, error) {
	if _, err := q.ensureLoggedIn(); err != nil {
		return "", err
	}
	return q.AppVersion(), nil
}

func (q *Connection) setToggleFlag(path string, hash Hash, newState bool, current func(*TorrentInfo) bool) error {
	item := q.TorrentsList.ByHash(hash)
	if item == nil {
		return fmt.Errorf("%w: %s", ErrTorrentNotFound, path)
	}
	if current(item) == newState {
		return nil
	}
	_, err := q.PostForm(path, url.Values{"hashes": {string(hash)}})
	return err
}

func (q *Connection) SetSequentialDownload(hash Hash, newState bool) error {
	return q.setToggleFlag("torrents/toggleSequentialDownload", hash, newState,
		func(t *TorrentInfo) bool { return t.Seq_dl })
}

func (q *Connection) SetFirstLastPieceFirst(hash Hash, newState bool) error {
	return q.setToggleFlag("torrents/toggleFirstLastPiecePrio", hash, newState,
		func(t *TorrentInfo) bool { return t.F_l_piece_prio })
}

// UpdateTorrentsList refreshes the torrent table from sync/maindata (or
// torrents/info without sync). Concurrent calls are serialised.
func (q *Connection) UpdateTorrentsList() error {
	q.updateMu.Lock()
	defer q.updateMu.Unlock()

	list := &q.TorrentsList
	list.mutex.RLock()
	useSync, rid := list.useSync, list.rid
	list.mutex.RUnlock()

	if !useSync {
		var torrents TorrentInfoList
		if err := q.getJSON("torrents/info", nil, &torrents); err != nil {
			return err
		}
		list.replaceAll(torrents)
		return nil
	}

	data, err := q.Get("sync/maindata", map[string]string{"rid": strconv.Itoa(rid)})
	if err != nil {
		return err
	}
	var mainData MainData
	if err := json.Unmarshal(data, &mainData); err != nil {
		return decodeError("sync/maindata", data, err)
	}
	if err := list.applyMainData(&mainData); err != nil {
		return decodeError("sync/maindata", data, err)
	}
	return nil
}

func joinHashes(hashes []Hash) string {
	s := make([]string, len(hashes))
	for i, h := range hashes {
		s[i] = string(h)
	}
	return strings.Join(s, "|")
}
