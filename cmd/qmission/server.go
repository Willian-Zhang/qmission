package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	runtimedebug "runtime/debug"
	"strconv"
	"time"

	"github.com/Willian-Zhang/qmission/transmission"
	log "github.com/sirupsen/logrus"
)

const sessionIDHeader = "X-Transmission-Session-Id"

// errNotImplemented is the result for methods and writes qmission can't do yet.
var errNotImplemented = errors.New("not implemented in qmission yet")

// Bridge Basic-auth credentials, set from QMISSION_USER/QMISSION_PASS in main.
var (
	bridgeUser string
	bridgePass string
)

// sessionID is the X-Transmission-Session-Id for this process. Like Transmission,
// it changes only on restart; a client with a stale one gets a 409 with the new one.
var sessionID = newSessionID()

func newSessionID() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

const maxRequestBody = 64 << 20 // base64 metainfo of big .torrent files

type rpcHandler func(args json.RawMessage) (JsonMap, error)

// rpcMethods maps Transmission methods to handlers. Methods not listed here
// (session-set, torrent-set-location's siblings, queue-*, group-*, ...) answer
// errNotImplemented.
var rpcMethods = map[string]rpcHandler{
	"session-get":          SessionGet,
	"session-stats":        SessionStats,
	"torrent-get":          TorrentGet,
	"torrent-stop":         TorrentStop,
	"torrent-start":        TorrentStart,
	"torrent-verify":       TorrentRecheck,
	"torrent-remove":       TorrentDelete,
	"torrent-add":          TorrentAdd,
	"torrent-set":          TorrentSet,
	"torrent-set-location": TorrentSetLocation,
}

func credentialsMatch(user, pass string) bool {
	hash := func(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }
	userOK := subtle.ConstantTimeCompare(hash(user), hash(bridgeUser))
	passOK := subtle.ConstantTimeCompare(hash(pass), hash(bridgePass))
	return userOK&passOK == 1
}

func newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/transmission/rpc", handler)
	mux.HandleFunc("/rpc", handler)
	mux.HandleFunc("/healthz", healthzHandler)
	mux.Handle("/", http.FileServer(http.Dir("web/")))
	return mux
}

// handler serves the Transmission RPC endpoint: Basic auth, then the
// X-Transmission-Session-Id check (409), then the JSON request. Failures of the
// request itself are answered like Transmission does: HTTP 200 with the error
// message in `result`.
func handler(w http.ResponseWriter, r *http.Request) {
	user, pass, ok := r.BasicAuth()
	if !ok || !credentialsMatch(user, pass) {
		if ok {
			log.WithField("remote", r.RemoteAddr).Warn("Rejected RPC request with wrong bridge credentials")
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="qmission"`)
		http.Error(w, "401: Unauthorized", http.StatusUnauthorized)
		return
	}

	if r.Header.Get(sessionIDHeader) != sessionID {
		w.Header().Set(sessionIDHeader, sessionID)
		w.WriteHeader(http.StatusConflict)
		fmt.Fprintf(w, "<h1>409: Conflict</h1><p>Your request had an invalid session-id header.</p>"+
			"<p><code>%s: %s</code></p>", sessionIDHeader, sessionID)
		return
	}
	w.Header().Set(sessionIDHeader, sessionID)

	reqBody, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err != nil {
		writeRPCResponse(w, nil, nil, fmt.Errorf("reading request: %w", err))
		return
	}
	log.Debug("Got request ", string(reqBody))

	var req transmission.RPCRequest
	if err := json.Unmarshal(reqBody, &req); err != nil {
		writeRPCResponse(w, nil, nil, fmt.Errorf("invalid JSON request: %w", err))
		return
	}

	resp, err := dispatch(req)
	writeRPCResponse(w, req.Tag, resp, err)
}

// dispatch runs one RPC method. A panic anywhere below is logged and turned
// into an error result so one bad request can't take the connection down.
func dispatch(req transmission.RPCRequest) (resp JsonMap, err error) {
	defer func() {
		if p := recover(); p != nil {
			log.WithField("method", req.Method).Errorf("Panic in RPC handler: %v\n%s", p, runtimedebug.Stack())
			resp, err = nil, fmt.Errorf("internal error in %s: %v", req.Method, p)
		}
	}()
	h, ok := rpcMethods[req.Method]
	if !ok {
		log.WithField("method", req.Method).Info("Unsupported RPC method")
		return nil, errNotImplemented
	}
	if len(req.Arguments) == 0 || string(req.Arguments) == "null" {
		req.Arguments = json.RawMessage("{}")
	}
	return h(req.Arguments)
}

func writeRPCResponse(w http.ResponseWriter, tag json.RawMessage, args JsonMap, err error) {
	result := "success"
	if err != nil {
		result = err.Error()
		args = JsonMap{}
		log.WithError(err).Info("RPC request failed")
	}
	if args == nil {
		args = JsonMap{}
	}
	response := JsonMap{
		"result":    result,
		"arguments": args,
	}
	if len(tag) > 0 && string(tag) != "null" {
		response["tag"] = tag
	}
	respBody, mErr := json.Marshal(response)
	if mErr != nil {
		log.WithError(mErr).Error("Encoding RPC response")
		respBody, _ = json.Marshal(JsonMap{"result": "internal error encoding the response", "arguments": JsonMap{}})
	}
	log.Debug("respBody: ", string(respBody))
	w.Header().Set("Content-Length", strconv.Itoa(len(respBody)))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if _, err := w.Write(respBody); err != nil {
		log.WithError(err).Debug("Writing RPC response")
	}
}

// healthTimeout stays below the compose healthcheck timeout (10 s).
var healthTimeout = 8 * time.Second

// healthzHandler (no bridge auth) answers 200 "ok" when qBT accepts our login and
// a cheap API call, 503 otherwise. Used by the compose healthcheck.
func healthzHandler(w http.ResponseWriter, r *http.Request) {
	done := make(chan error, 1)
	go func() {
		_, err := qBTConn.Get("app/webapiVersion", nil)
		done <- err
	}()
	var err error
	select {
	case err = <-done:
	case <-time.After(healthTimeout):
		err = errors.New("qBittorrent did not answer in time")
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		log.WithError(err).Info("Health check failed")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintln(w, "qBittorrent unavailable:", err)
		return
	}
	io.WriteString(w, "ok")
}
