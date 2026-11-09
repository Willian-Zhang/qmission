package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Willian-Zhang/qmission/qBT"
	"github.com/Willian-Zhang/qmission/transmission"
	"gopkg.in/h2non/gock.v1"
)

// Synthetic torrents for the handler tests (no real torrent data).
const syntheticMainData = `{
  "rid": 1, "full_update": true,
  "server_state": {"alltime_dl": 1000, "alltime_ul": 500, "dl_info_data": 10, "up_info_data": 5,
                   "dl_info_speed": 3, "up_info_speed": 4, "free_space_on_disk": 123456},
  "torrents": {
    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa": {"name": "A", "state": "downloading", "progress": 0.5,
       "tags": "x, y", "eta": 8640000, "priority": 2, "tracker": "https://t.example.org/announce",
       "num_complete": 5, "num_incomplete": 6, "completed": 100, "amount_left": 100, "size": 200},
    "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb": {"name": "B", "state": "stoppedUP", "progress": 1,
       "tags": "", "eta": 8640000, "priority": 0, "tracker": "", "trackers_count": 0, "completion_on": 4294967295}
  }
}`

const hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func setUpSynthetic(t *testing.T) *rpcClient {
	t.Helper()
	quietLogs(t)
	t.Cleanup(gock.Off)
	mockTestLogin("2.16.2", "v5.3.0")
	gock.New(testQBTAddr).Get("/api/v2/sync/maindata").MatchParam("rid", "^0$").Reply(200).BodyString(syntheticMainData)
	gock.New(testQBTAddr).Get("/api/v2/sync/maindata").MatchParam("rid", "^1$").Persist().Reply(200).BodyString(`{"rid": 1}`)
	initTestQBT(t, true)
	_, c := newTestServer(t)
	return c
}

func TestAuthAndSessionID(t *testing.T) {
	quietLogs(t)
	_, c := newTestServer(t)
	body := []byte(`{"method":"session-get"}`)

	resp := c.post("/transmission/rpc", body, false, "")
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") != `Basic realm="qmission"` {
		t.Errorf("no credentials: HTTP %d, WWW-Authenticate %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
	resp.Body.Close()

	req, _ := http.NewRequest("POST", c.url+"/rpc", strings.NewReader(string(body)))
	req.SetBasicAuth(testBridgeUser, "wrong")
	resp, err := c.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong password: HTTP %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Authenticated but no session id: 409 with the id, then a stale id: 409 again.
	resp = c.post("/transmission/rpc", body, true, "")
	sid := resp.Header.Get(sessionIDHeader)
	if resp.StatusCode != http.StatusConflict || sid == "" || sid != sessionID {
		t.Errorf("no session id: HTTP %d, header %q", resp.StatusCode, sid)
	}
	resp.Body.Close()
	resp = c.post("/rpc", body, true, "stale")
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("stale session id: HTTP %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Valid id passes the handshake (the method itself fails: no qBT mocks).
	t.Cleanup(gock.Off)
	initTestQBT(t, true)
	resp = c.post("/rpc", body, true, sid)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("valid session id: HTTP %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestUnimplementedMethods(t *testing.T) {
	c := setUpSynthetic(t)
	for _, method := range []string{
		"session-set", "torrent-start-now", "torrent-reannounce", "torrent-rename-path", "free-space",
		"queue-move-top", "queue-move-up", "queue-move-down", "queue-move-bottom", "port-test",
		"blocklist-update", "group-get", "group-set", "session-close", "no-such-method",
	} {
		out := c.call(method, map[string]interface{}{"ids": []int{1}})
		if out["result"] != errNotImplemented.Error() {
			t.Errorf("%s: result = %v", method, out["result"])
		}
	}
	// torrent-set with anything but files-wanted/unwanted fails instead of faking success.
	out := c.call("torrent-set", map[string]interface{}{"ids": []int{1}, "labels": []string{"x"}})
	if out["result"] != errNotImplemented.Error() {
		t.Errorf("torrent-set labels: result = %v", out["result"])
	}
}

func TestPanicBecomesErrorResult(t *testing.T) {
	quietLogs(t)
	rpcMethods["test-panic"] = func(json.RawMessage) (JsonMap, error) { panic("boom") }
	defer delete(rpcMethods, "test-panic")
	_, c := newTestServer(t)
	out := c.call("test-panic", nil)
	if res, _ := out["result"].(string); !strings.Contains(res, "internal error") || !strings.Contains(res, "boom") {
		t.Errorf("result = %v", out["result"])
	}
}

func TestInvalidJSONRequest(t *testing.T) {
	quietLogs(t)
	_, c := newTestServer(t)
	resp := c.post("/rpc", []byte(`{`), true, sessionID)
	defer resp.Body.Close()
	var out map[string]interface{}
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || json.Unmarshal(data, &out) != nil || !strings.HasPrefix(out["result"].(string), "invalid JSON request") {
		t.Errorf("HTTP %d, body %s", resp.StatusCode, data)
	}
}

func TestQBTErrorBecomesResult(t *testing.T) {
	quietLogs(t)
	t.Cleanup(gock.Off)
	mockTestLogin("2.9.3", "v4.6.4")
	gock.New(testQBTAddr).Get("/api/v2/app/preferences").Reply(500).BodyString("oops")
	initTestQBT(t, true)
	_, c := newTestServer(t)
	out := c.call("session-get", nil)
	if res, _ := out["result"].(string); !strings.Contains(res, "HTTP 500") {
		t.Errorf("result = %v", out["result"])
	}
	if args := out["arguments"].(map[string]interface{}); len(args) != 0 {
		t.Errorf("arguments = %v, want {}", args)
	}
}

func TestParseIDs(t *testing.T) {
	c := setUpSynthetic(t)
	ids := func(args map[string]interface{}) []float64 {
		t.Helper()
		out := c.mustSucceed("torrent-get", args)
		var got []float64
		for _, tor := range out["torrents"].([]interface{}) {
			got = append(got, tor.(map[string]interface{})["id"].(float64))
		}
		return got
	}
	fields := []string{"id"}
	tests := []struct {
		name string
		ids  interface{}
		want []float64
	}{
		{"absent", nil, []float64{1, 2}},
		{"single", 2, []float64{2}},
		{"single unknown", 99, nil},
		{"list with unknown id and hash", []interface{}{99, 1, "ffffffffffffffffffffffffffffffffffffffff"}, []float64{1}},
		{"hash, upper case", strings.ToUpper(hashB), []float64{2}},
		{"list of hashes", []interface{}{hashB, hashA}, []float64{2, 1}},
		{"empty list", []interface{}{}, nil},
		{"recently-active", "recently-active", []float64{1, 2}},
	}
	for _, tt := range tests {
		args := map[string]interface{}{"fields": fields}
		if tt.ids != nil {
			args["ids"] = tt.ids
		}
		if got := ids(args); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: ids = %v, want %v", tt.name, got, tt.want)
		}
	}

	out := c.mustSucceed("torrent-get", map[string]interface{}{"fields": fields, "ids": "recently-active"})
	if _, ok := out["removed"].([]interface{}); !ok {
		t.Errorf("recently-active: no removed list: %v", out)
	}
	if bad := c.call("torrent-get", map[string]interface{}{"fields": fields, "ids": map[string]int{"x": 1}}); bad["result"] == "success" {
		t.Error("object ids: expected an error")
	}
}

func TestTorrentGetUnknownAndTemplateFields(t *testing.T) {
	c := setUpSynthetic(t)
	fields := []string{"id", "noSuchField", "corruptEver", "peersFrom", "labels", "sequential_download", "eta", "doneDate", "trackerStats"}
	out := c.mustSucceed("torrent-get", map[string]interface{}{"fields": fields})
	torrents := out["torrents"].([]interface{})
	if len(torrents) != 2 {
		t.Fatalf("%d torrents", len(torrents))
	}
	a := torrents[0].(map[string]interface{})
	if _, ok := a["noSuchField"]; ok {
		t.Error("unknown field should be omitted")
	}
	if a["corruptEver"] != 0.0 {
		t.Errorf("corruptEver = %v", a["corruptEver"])
	}
	if pf, ok := a["peersFrom"].(map[string]interface{}); !ok || len(pf) != 7 {
		t.Errorf("peersFrom = %v", a["peersFrom"])
	}
	if !reflect.DeepEqual(a["labels"], []interface{}{"x", "y"}) {
		t.Errorf("labels = %v", a["labels"])
	}
	if a["eta"] != -2.0 {
		t.Errorf("downloading eta = %v, want -2", a["eta"])
	}
	stats := a["trackerStats"].([]interface{})
	if len(stats) != 1 || stats[0].(map[string]interface{})["host"] != "t.example.org:443" ||
		stats[0].(map[string]interface{})["sitename"] != "example" || stats[0].(map[string]interface{})["seederCount"] != 5.0 {
		t.Errorf("trackerStats = %v", stats)
	}
	b := torrents[1].(map[string]interface{})
	if b["eta"] != -1.0 || b["doneDate"] != 0.0 {
		t.Errorf("stopped torrent eta = %v, doneDate = %v", b["eta"], b["doneDate"])
	}
	if !reflect.DeepEqual(b["labels"], []interface{}{}) || len(b["trackerStats"].([]interface{})) != 0 {
		t.Errorf("labels = %v, trackerStats = %v", b["labels"], b["trackerStats"])
	}

	// table format: header row + one row per torrent.
	out = c.mustSucceed("torrent-get", map[string]interface{}{"fields": []string{"id", "name"}, "format": "table"})
	table := out["torrents"].([]interface{})
	if len(table) != 3 || !reflect.DeepEqual(table[0], []interface{}{"id", "name"}) {
		t.Errorf("table = %v", table)
	}
}

func TestHealthz(t *testing.T) {
	quietLogs(t)
	t.Cleanup(gock.Off)
	srv, c := newTestServer(t)
	get := func() (int, string) {
		resp, err := c.client.Get(srv.URL + "/healthz")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	mockTestLogin("2.16.2", "v5.3.0")
	initTestQBT(t, true)
	if code, body := get(); code != 200 || body != "ok" {
		t.Errorf("healthy: %d %q", code, body)
	}

	gock.Off()
	gock.New(testQBTAddr).Post("/api/v2/auth/login").Persist().Reply(401)
	initTestQBT(t, true)
	if code, _ := get(); code != http.StatusServiceUnavailable {
		t.Errorf("bad credentials: %d", code)
	}

	gock.Off()
	gock.New(testQBTAddr).Post("/api/v2/auth/login").Persist().ReplyError(errors.New("connection refused"))
	initTestQBT(t, true)
	if code, _ := get(); code != http.StatusServiceUnavailable {
		t.Errorf("qBT down: %d", code)
	}
}

func TestLoginLoopRetries(t *testing.T) {
	quietLogs(t)
	t.Cleanup(gock.Off)
	prev := loginRetryInitial
	loginRetryInitial = 10 * time.Millisecond
	defer func() { loginRetryInitial = prev }()

	gock.New(testQBTAddr).Post("/api/v2/auth/login").Times(2).ReplyError(errors.New("connection refused"))
	mockTestLogin("2.9.3", "v4.6.4")
	initTestQBT(t, true)
	done := make(chan struct{})
	go func() { loginLoop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("loginLoop did not finish")
	}
	if !qBTConn.IsLoggedIn() || qBTConn.AppVersion() != "v4.6.4" {
		t.Errorf("logged in = %v, version %q", qBTConn.IsLoggedIn(), qBTConn.AppVersion())
	}
}

func TestLoadConfig(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	_, err := loadConfig(env(map[string]string{"QBT_USER": "u", "QBT_PASS": "p"}))
	if !errors.Is(err, errNoBridgeCredentials) ||
		err.Error() != "bridge credentials required (QMISSION_USER/QMISSION_PASS); passthrough auth not implemented yet" {
		t.Errorf("err = %v", err)
	}
	c, err := loadConfig(env(map[string]string{"QBT_USER": "u", "QBT_PASS": "p", "QMISSION_USER": "b", "QMISSION_PASS": "c"}))
	if err != nil || c.qbtUser != "u" || c.qbtPass != "p" || c.bridgeUser != "b" || c.bridgePass != "c" {
		t.Errorf("config = %+v, %v", c, err)
	}
}

func TestStateMapping(t *testing.T) {
	tests := []struct {
		state    string
		progress float64
		status   int
		errCode  int
		stalled  bool
	}{
		{"error", 0.5, 0, 3, false},
		{"missingFiles", 1, 0, 3, false},
		{"uploading", 1, 6, 0, false},
		{"stoppedUP", 1, 0, 0, false},
		{"pausedUP", 1, 0, 0, false},
		{"queuedUP", 1, 5, 0, false},
		{"stalledUP", 1, 6, 0, true},
		{"checkingUP", 1, 2, 0, false},
		{"forcedUP", 1, 6, 0, false},
		{"allocating", 0, 4, 0, false},
		{"downloading", 0.1, 4, 0, false},
		{"metaDL", 0, 4, 0, false},
		{"forcedMetaDL", 0, 4, 0, false},
		{"stoppedDL", 0.1, 0, 0, false},
		{"pausedDL", 0.1, 0, 0, false},
		{"queuedDL", 0.1, 3, 0, false},
		{"stalledDL", 0.1, 4, 0, true},
		{"checkingDL", 0.1, 2, 0, false},
		{"forcedDL", 0.1, 4, 0, false},
		{"checkingResumeData", 0.1, 1, 0, false},
		{"moving", 0.5, 4, 0, false},
		{"moving", 1, 6, 0, false},
		{"unknown", 0, 0, 0, false},
		{"somethingNew", 0, 0, 0, false},
	}
	for _, tt := range tests {
		if got := qBTStateToTransmissionStatus(tt.state, tt.progress); got != tt.status {
			t.Errorf("%s: status %d, want %d", tt.state, got, tt.status)
		}
		if code, msg := qBTStateToTransmissionError(tt.state); code != tt.errCode || (code != 0) != (msg != "") {
			t.Errorf("%s: error %d %q", tt.state, code, msg)
		}
		if got := qBTStateToTransmissionStalled(tt.state); got != tt.stalled {
			t.Errorf("%s: stalled %v", tt.state, got)
		}
	}
}

func TestFieldHelpers(t *testing.T) {
	etas := []struct {
		eta    int64
		status int
		want   int64
	}{
		{8640000, TR_STATUS_DOWNLOAD, -2}, {8640000, TR_STATUS_SEED, -1}, {8640000, TR_STATUS_STOPPED, -1},
		{-1, TR_STATUS_DOWNLOAD, -1}, {120, TR_STATUS_DOWNLOAD, 120},
	}
	for _, e := range etas {
		if got := mapETA(e.eta, e.status); got != e.want {
			t.Errorf("mapETA(%d, %d) = %d, want %d", e.eta, e.status, got, e.want)
		}
	}
	for in, want := range map[int64]int64{4294967295: 0, -1: 0, 0: 0, -28800: 0, 1794244959: 1794244959} {
		if got := mapDate(in); got != want {
			t.Errorf("mapDate(%d) = %d", in, got)
		}
	}
	hosts := []struct{ url, host, site string }{
		{"udp://tracker1.example/announce", "tracker1.example:80", "tracker1"},
		{"https://www.example.co.uk/announce/", "www.example.co.uk:443", "example"},
		{"http://Tracker.Example.org:6969/announce", "tracker.example.org:6969", "example"},
		{"wss://tracker6.example/announce", "tracker6.example:443", "tracker6"},
		{"udp://10.0.0.1:1337/announce", "10.0.0.1:1337", "10.0.0.1"},
		{"http://[2001:db8::1]/announce", "[2001:db8::1]:80", "2001:db8::1"},
	}
	for _, h := range hosts {
		if host, site := trackerHost(h.url); host != h.host || site != h.site {
			t.Errorf("trackerHost(%q) = %q, %q; want %q, %q", h.url, host, site, h.host, h.site)
		}
	}
	if got := parseTags(" a, b ,,c "); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("parseTags = %q", got)
	}
	if got := parseTags(""); got == nil || len(got) != 0 {
		t.Errorf("parseTags(\"\") = %#v, want empty non-nil", got)
	}
	for in, want := range map[int64][2]interface{}{0: {int64(0), false}, -1: {int64(0), false}, 512000: {int64(500), true}, 100: {int64(1), true}} {
		if kib, on := bytesToKiB(in); kib != want[0] || on != want[1] {
			t.Errorf("bytesToKiB(%d) = %d, %v", in, kib, on)
		}
	}
	for in, want := range map[int]int{0: 127, 1: 62, 2: 65, 3: 2, 8: 64, 9: 1} {
		if got := qBTSchedulerDaysToTR(in); got != want {
			t.Errorf("scheduler days %d = %d, want %d", in, got, want)
		}
	}
	list := qBT.TorrentInfoList{
		{Id: 1, Hash: "1", Priority: 0}, {Id: 2, Hash: "2", Priority: 2}, {Id: 3, Hash: "3", Priority: 1}, {Id: 4, Hash: "4", Priority: 0},
	}
	if got := queuePositions(list); !reflect.DeepEqual(got, map[qBT.Hash]int{"3": 0, "2": 1, "1": 2, "4": 3}) {
		t.Errorf("queuePositions = %v", got)
	}
	if qmissionVersion("v5.3.0") != "4.1.0 (qmission, qBT 5.3.0)" {
		t.Errorf("version = %q", qmissionVersion("v5.3.0"))
	}
	// Every default is JSON-encodable and the template keeps the spec's units.
	if _, err := json.Marshal(transmission.TorrentGetDefaults()); err != nil {
		t.Error(err)
	}
}
