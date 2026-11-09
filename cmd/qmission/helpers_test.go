package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	log "github.com/sirupsen/logrus"
	"gopkg.in/h2non/gock.v1"
)

const (
	testBridgeUser = "bridge-user"
	testBridgePass = "bridge-pass"
	testQBTAddr    = "http://qbt.test:8080"
)

func TestMain(m *testing.M) {
	bridgeUser, bridgePass = testBridgeUser, testBridgePass
	os.Exit(m.Run())
}

// initTestQBT points the shared qBTConn at gock-mocked testQBTAddr.
func initTestQBT(t *testing.T, sync bool) {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{}}
	gock.InterceptClient(client)
	if err := qBTConn.Init(testQBTAddr+"/", client, sync); err != nil {
		t.Fatal(err)
	}
	qBTConn.SetCredentials("user", "pass")
	prevSync := *useSync
	*useSync = sync
	t.Cleanup(func() { *useSync = prevSync })
	resetCaches()
}

func mockTestLogin(webapi, app string) {
	gock.New(testQBTAddr).Post("/api/v2/auth/login").Persist().Reply(200).
		SetHeader("Set-Cookie", "SID=test; HttpOnly; path=/").BodyString("Ok.")
	gock.New(testQBTAddr).Get("/api/v2/app/webapiVersion").Persist().Reply(200).BodyString(webapi)
	gock.New(testQBTAddr).Get("/api/v2/app/version").Persist().Reply(200).BodyString(app)
}

// rpcClient talks to the bridge like TrguiNG: Basic auth plus the 409 session-id retry.
type rpcClient struct {
	t      *testing.T
	url    string
	sid    string
	client *http.Client
}

func newTestServer(t *testing.T) (*httptest.Server, *rpcClient) {
	t.Helper()
	srv := httptest.NewServer(newMux())
	t.Cleanup(srv.Close)
	// Own transport: gock replaces http.DefaultTransport.
	return srv, &rpcClient{t: t, url: srv.URL, client: &http.Client{Transport: &http.Transport{}}}
}

func (c *rpcClient) post(path string, body []byte, auth bool, sid string) *http.Response {
	c.t.Helper()
	req, err := http.NewRequest("POST", c.url+path, bytes.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	if auth {
		req.SetBasicAuth(testBridgeUser, testBridgePass)
	}
	if sid != "" {
		req.Header.Set(sessionIDHeader, sid)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	return resp
}

// call sends one RPC request and returns the decoded response. It fails the
// test unless the answer is HTTP 200 JSON.
func (c *rpcClient) call(method string, args interface{}) map[string]interface{} {
	c.t.Helper()
	req := map[string]interface{}{"method": method, "tag": 7}
	if args != nil {
		req["arguments"] = args
	}
	body, _ := json.Marshal(req)
	resp := c.post("/transmission/rpc", body, true, c.sid)
	if resp.StatusCode == http.StatusConflict {
		c.sid = resp.Header.Get(sessionIDHeader)
		resp.Body.Close()
		resp = c.post("/transmission/rpc", body, true, c.sid)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		c.t.Fatalf("%s: HTTP %d", method, resp.StatusCode)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		c.t.Fatalf("%s: invalid JSON response: %v", method, err)
	}
	if tag, _ := out["tag"].(float64); tag != 7 {
		c.t.Errorf("%s: tag = %v, want 7", method, out["tag"])
	}
	if _, ok := out["arguments"].(map[string]interface{}); !ok {
		c.t.Errorf("%s: arguments missing or not an object", method)
	}
	return out
}

// mustSucceed calls a method and returns its arguments, failing on a non-success result.
func (c *rpcClient) mustSucceed(method string, args interface{}) map[string]interface{} {
	c.t.Helper()
	out := c.call(method, args)
	if out["result"] != "success" {
		c.t.Fatalf("%s: result = %v", method, out["result"])
	}
	return out["arguments"].(map[string]interface{})
}

// quietLogs lowers the log level for the test (handlers log at debug).
func quietLogs(t *testing.T) {
	prev := log.GetLevel()
	log.SetLevel(log.WarnLevel)
	t.Cleanup(func() { log.SetLevel(prev) })
}
