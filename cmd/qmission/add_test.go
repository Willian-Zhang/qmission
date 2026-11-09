package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Willian-Zhang/qmission/qBT"
	"gopkg.in/h2non/gock.v1"
)

// --- request capture -------------------------------------------------------

// captured records the form of every request a mock answered.
type captured struct {
	mu    sync.Mutex
	forms []url.Values // multipart file parts appear as "<field>@file"
}

func (c *captured) all() []url.Values {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]url.Values{}, c.forms...)
}

func (c *captured) count() int { return len(c.all()) }

// capturePost registers a persisted POST mock for path that records each
// request's form. It uses a private matcher: gock v1.0.14's AddMatcher appends
// to the shared DefaultMatcher and would affect every other mock.
func capturePost(t *testing.T, path string, status int, body string) *captured {
	t.Helper()
	c := &captured{}
	m := gock.NewEmptyMatcher()
	m.Matchers = append(append([]gock.MatchFunc{}, gock.Matchers...), func(req *http.Request, _ *gock.Request) (bool, error) {
		data, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(data))
		form := url.Values{}
		mediaType, params, _ := mime.ParseMediaType(req.Header.Get("Content-Type"))
		if mediaType == "multipart/form-data" {
			r := multipart.NewReader(bytes.NewReader(data), params["boundary"])
			for {
				part, err := r.NextPart()
				if err != nil {
					break
				}
				value, _ := io.ReadAll(part)
				if part.FileName() != "" {
					form.Add(part.FormName()+"@file", string(value))
				} else {
					form.Add(part.FormName(), string(value))
				}
			}
		} else {
			form, _ = url.ParseQuery(string(data))
		}
		c.mu.Lock()
		c.forms = append(c.forms, form)
		c.mu.Unlock()
		return true, nil
	})
	gock.New(testQBTAddr).Post(path).SetMatcher(m).Persist().Reply(status).BodyString(body)
	return c
}

// --- dataset helpers -------------------------------------------------------

// forEachDatasetWith is forEachDataset with a hook that registers mocks before
// the recording's own; gock matches mocks in registration order.
func forEachDatasetWith(t *testing.T, pre func(t *testing.T, ds *dataset), fn func(t *testing.T, ds *dataset, c *rpcClient)) {
	for _, name := range datasetDirs {
		name := name
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join("testdata", name)
			if _, err := os.Stat(dir); err != nil {
				t.Skipf("no recording in %s", dir)
			}
			quietLogs(t)
			t.Cleanup(gock.Off)
			ds := loadDataset(t, name, dir)
			if pre != nil {
				pre(t, ds)
			}
			ds.mock(t)
			initTestQBT(t, true)
			_, c := newTestServer(t)
			fn(t, ds, c)
		})
	}
}

func (ds *dataset) lastRid() int { return int(ds.syncFiles[len(ds.syncFiles)-1]["rid"].(float64)) }

func (ds *dataset) gates(t *testing.T) qBT.Gates {
	v, err := qBT.ParseAPIVersion(ds.webapi)
	if err != nil {
		t.Fatal(err)
	}
	return qBT.GatesFor(v)
}

// addsListIDs: WebAPI >= 2.14 answers torrents/add with JSON (added_torrent_ids).
func (ds *dataset) addsListIDs(t *testing.T) bool {
	v, _ := qBT.ParseAPIVersion(ds.webapi)
	return v.AtLeast(qBT.APIVersion{Major: 2, Minor: 14})
}

// addReply is what this qBT version answers to a successful torrents/add.
func (ds *dataset) addReply(t *testing.T, hash string, pending bool) (int, string) {
	if !ds.addsListIDs(t) {
		return 200, "Ok."
	}
	if pending {
		return 202, `{"success_count":0,"failure_count":0,"pending_count":1,"added_torrent_ids":[]}`
	}
	return 200, fmt.Sprintf(`{"success_count":1,"failure_count":0,"pending_count":0,"added_torrent_ids":[%q]}`, hash)
}

// appearsAfterChain makes `hash` show up in the sync delta after the recorded
// ones, i.e. a few polls after the add.
func appearsAfterChain(ds *dataset, hash, name string) {
	last := ds.lastRid()
	gock.New(testQBTAddr).Get("/api/v2/sync/maindata").MatchParam("rid", fmt.Sprintf("^%d$", last)).
		Reply(200).BodyString(fmt.Sprintf(`{"rid": %d, "torrents": {%q: {"name": %q, "state": "metaDL", "infohash_v1": %q, "tags": ""}}}`,
		last+1, hash, name, hash))
	gock.New(testQBTAddr).Get("/api/v2/sync/maindata").MatchParam("rid", fmt.Sprintf("^%d$", last+1)).
		Persist().Reply(200).BodyString(fmt.Sprintf(`{"rid": %d}`, last+1))
}

func randomHash(t *testing.T) []byte {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// makeTorrent builds a canonical multi-file v1 .torrent and returns it with its
// info hash, computed over the raw info bytes.
func makeTorrent(name string, sizes ...int) ([]byte, string) {
	var info strings.Builder
	info.WriteString("d5:filesl")
	for i, size := range sizes {
		path := fmt.Sprintf("f%d.bin", i)
		fmt.Fprintf(&info, "d6:lengthi%de4:pathl%d:%see", size, len(path), path)
	}
	fmt.Fprintf(&info, "e4:name%d:%s12:piece lengthi16384e6:pieces20:%se", len(name), name, strings.Repeat("\x01", 20))
	announce := "http://tracker.invalid/announce"
	torrent := fmt.Sprintf("d8:announce%d:%s4:info%se", len(announce), announce, info.String())
	sum := sha1.Sum([]byte(info.String()))
	return []byte(torrent), hex.EncodeToString(sum[:])
}

// torrentID looks up the Transmission id of hash via torrent-get.
func torrentID(t *testing.T, c *rpcClient, hash string) float64 {
	t.Helper()
	out := c.mustSucceed("torrent-get", map[string]interface{}{"ids": []string{hash}, "fields": []string{"id", "hashString"}})
	list := out["torrents"].([]interface{})
	if len(list) != 1 {
		t.Fatalf("torrent-get %s: %d torrents", hash, len(list))
	}
	return list[0].(map[string]interface{})["id"].(float64)
}

func checkAdded(t *testing.T, c *rpcClient, args map[string]interface{}, key, hash, name string) {
	t.Helper()
	added, ok := args[key].(map[string]interface{})
	if !ok {
		t.Fatalf("no %s in %v", key, args)
	}
	if added["hashString"] != hash {
		t.Errorf("%s hashString = %v, want %s", key, added["hashString"], hash)
	}
	if name != "" && added["name"] != name {
		t.Errorf("%s name = %v, want %s", key, added["name"], name)
	}
	if id := torrentID(t, c, hash); added["id"] != id {
		t.Errorf("%s id = %v, torrent-get id = %v", key, added["id"], id)
	}
}

func expectForm(t *testing.T, form url.Values, want map[string]string, absent ...string) {
	t.Helper()
	for k, v := range want {
		if got := form.Get(k); got != v {
			t.Errorf("form %s = %q, want %q", k, got, v)
		}
	}
	for _, k := range absent {
		if _, ok := form[k]; ok {
			t.Errorf("form has %s = %q, want it absent", k, form.Get(k))
		}
	}
}

// --- torrent-add -------------------------------------------------------------

func TestTorrentAddMagnetHex(t *testing.T) {
	hash := hex.EncodeToString(randomHash(t))
	var add *captured
	forEachDatasetWith(t, func(t *testing.T, ds *dataset) {
		status, body := ds.addReply(t, hash, false)
		add = capturePost(t, "/api/v2/torrents/add", status, body)
		appearsAfterChain(ds, hash, "magnet-hex")
	}, func(t *testing.T, ds *dataset, c *rpcClient) {
		magnet := "magnet:?xt=urn:btih:" + strings.ToUpper(hash) + "&dn=magnet-hex&tr=http%3A%2F%2Ftracker.invalid%2Fa"
		// The arguments TrguiNG's add dialog sends, including its own extra keys.
		args := c.mustSucceed("torrent-add", map[string]interface{}{
			"filename": magnet, "download-dir": "/data/dl", "labels": []string{"a", " b ", ""},
			"paused": true, "sequential_download": true, "bandwidthPriority": 0, "peer-limit": 50,
			"canRename": false, "name": "x",
		})
		checkAdded(t, c, args, "torrent-added", hash, "magnet-hex")
		forms := add.all()
		if len(forms) != 1 {
			t.Fatalf("%d torrents/add calls", len(forms))
		}
		expectForm(t, forms[0], map[string]string{
			"urls": magnet, "savepath": "/data/dl", "tags": "a,b", ds.gates(t).AddStoppedParam: "true",
			"sequentialDownload": "true", "firstLastPiecePrio": "true",
		}, "torrents@file", "cookie")
	})
}

func TestTorrentAddMagnetBase32(t *testing.T) {
	raw := randomHash(t)
	hash := hex.EncodeToString(raw)
	var add *captured
	forEachDatasetWith(t, func(t *testing.T, ds *dataset) {
		status, body := ds.addReply(t, hash, false)
		add = capturePost(t, "/api/v2/torrents/add", status, body)
		appearsAfterChain(ds, hash, "magnet-b32")
	}, func(t *testing.T, ds *dataset, c *rpcClient) {
		magnet := "magnet:?xt=urn:btih:" + base32.StdEncoding.EncodeToString(raw)
		args := c.mustSucceed("torrent-add", map[string]interface{}{"filename": magnet, "paused": 0, "sequential_download": false})
		checkAdded(t, c, args, "torrent-added", hash, "magnet-b32")
		forms := add.all()
		if len(forms) != 1 {
			t.Fatalf("%d torrents/add calls", len(forms))
		}
		expectForm(t, forms[0], map[string]string{"urls": magnet, ds.gates(t).AddStoppedParam: "false"},
			"tags", "savepath", "sequentialDownload", "firstLastPiecePrio")
	})
}

func TestTorrentAddURL(t *testing.T) {
	hash := hex.EncodeToString(randomHash(t))
	var add *captured
	forEachDatasetWith(t, func(t *testing.T, ds *dataset) {
		status, body := ds.addReply(t, "", true)
		add = capturePost(t, "/api/v2/torrents/add", status, body)
		appearsAfterChain(ds, hash, "from-url")
	}, func(t *testing.T, ds *dataset, c *rpcClient) {
		link := "https://indexer.invalid/get?id=1"
		args := c.mustSucceed("torrent-add", map[string]interface{}{"filename": link, "cookies": "k=v"})
		checkAdded(t, c, args, "torrent-added", hash, "from-url")
		forms := add.all()
		if len(forms) != 1 {
			t.Fatalf("%d torrents/add calls", len(forms))
		}
		// No paused given: qBT's own "add stopped" preference applies.
		expectForm(t, forms[0], map[string]string{"urls": link, "cookie": "k=v"}, "paused", "stopped", "torrents@file")
	})
}

func TestTorrentAddMetainfo(t *testing.T) {
	for _, paused := range []bool{false, true} {
		paused := paused
		t.Run(fmt.Sprintf("paused=%v", paused), func(t *testing.T) {
			torrent, hash := makeTorrent("meta-name", 100, 200, 300)
			var add, prio, start *captured
			forEachDatasetWith(t, func(t *testing.T, ds *dataset) {
				status, body := ds.addReply(t, hash, false)
				add = capturePost(t, "/api/v2/torrents/add", status, body)
				prio = capturePost(t, "/api/v2/torrents/filePrio", 200, "")
				start = capturePost(t, "/api/v2/"+ds.gates(t).StartEndpoint, 200, "")
				appearsAfterChain(ds, hash, "meta-name")
			}, func(t *testing.T, ds *dataset, c *rpcClient) {
				args := c.mustSucceed("torrent-add", map[string]interface{}{
					"metainfo": base64.StdEncoding.EncodeToString(torrent), "labels": []string{"l1"},
					"sequential_download": true, "files-unwanted": []int{0, 2}, "paused": paused,
					"download-dir": "/data/x",
				})
				checkAdded(t, c, args, "torrent-added", hash, "meta-name")

				forms := add.all()
				if len(forms) != 1 {
					t.Fatalf("%d torrents/add calls", len(forms))
				}
				// Added stopped either way; started only after the priorities are set.
				expectForm(t, forms[0], map[string]string{
					"torrents@file": string(torrent), "tags": "l1", "savepath": "/data/x",
					ds.gates(t).AddStoppedParam: "true", "sequentialDownload": "true", "firstLastPiecePrio": "true",
				}, "urls")
				prios := prio.all()
				if len(prios) != 1 {
					t.Fatalf("%d filePrio calls", len(prios))
				}
				expectForm(t, prios[0], map[string]string{"hash": hash, "id": "0|2", "priority": "0"})
				starts := start.all()
				if paused {
					if len(starts) != 0 {
						t.Errorf("paused add was started: %v", starts)
					}
				} else if len(starts) != 1 || starts[0].Get("hashes") != hash {
					t.Errorf("start calls = %v, want one for %s", starts, hash)
				}
			})
		})
	}
}

func TestTorrentAddMetainfoStartsByPreference(t *testing.T) {
	// files-unwanted without `paused`: start afterwards unless qBT adds stopped by default.
	torrent, hash := makeTorrent("pref", 10, 20)
	var start *captured
	forEachDatasetWith(t, func(t *testing.T, ds *dataset) {
		status, body := ds.addReply(t, hash, false)
		capturePost(t, "/api/v2/torrents/add", status, body)
		capturePost(t, "/api/v2/torrents/filePrio", 200, "")
		start = capturePost(t, "/api/v2/"+ds.gates(t).StartEndpoint, 200, "")
		appearsAfterChain(ds, hash, "pref")
	}, func(t *testing.T, ds *dataset, c *rpcClient) {
		c.mustSucceed("torrent-add", map[string]interface{}{
			"metainfo": base64.StdEncoding.EncodeToString(torrent), "files-unwanted": []int{1},
		})
		addStopped, _ := ds.prefs[ds.gates(t).AddStoppedPref].(bool)
		if want := map[bool]int{true: 0, false: 1}[addStopped]; start.count() != want {
			t.Errorf("%s=%v: %d start calls, want %d", ds.gates(t).AddStoppedPref, addStopped, start.count(), want)
		}
	})
}

func TestTorrentAddDuplicate(t *testing.T) {
	var add *captured
	forEachDatasetWith(t, func(t *testing.T, ds *dataset) {
		add = capturePost(t, "/api/v2/torrents/add", 200, "Ok.")
	}, func(t *testing.T, ds *dataset, c *rpcClient) {
		var existing string
		for hash := range ds.torrentsAfter(0) {
			existing = hash
			break
		}
		raw, err := hex.DecodeString(existing)
		if err != nil {
			t.Fatal(err)
		}
		for _, magnet := range []string{
			"magnet:?xt=urn:btih:" + strings.ToUpper(existing),
			"magnet:?xt=urn:btih:" + base32.StdEncoding.EncodeToString(raw),
		} {
			args := c.mustSucceed("torrent-add", map[string]interface{}{"filename": magnet})
			checkAdded(t, c, args, "torrent-duplicate", existing, "")
			if _, ok := args["torrent-added"]; ok {
				t.Error("duplicate also reported as torrent-added")
			}
		}
		if n := add.count(); n != 0 {
			t.Errorf("%d torrents/add calls for a duplicate", n)
		}
	})
}

func TestTorrentAddTimeout(t *testing.T) {
	prev := addTimeout
	addTimeout = 300 * time.Millisecond
	t.Cleanup(func() { addTimeout = prev })
	hash := hex.EncodeToString(randomHash(t))
	forEachDatasetWith(t, func(t *testing.T, ds *dataset) {
		status, body := ds.addReply(t, hash, false)
		capturePost(t, "/api/v2/torrents/add", status, body)
	}, func(t *testing.T, ds *dataset, c *rpcClient) {
		start := time.Now()
		out := c.call("torrent-add", map[string]interface{}{"filename": "magnet:?xt=urn:btih:" + hash})
		if res, _ := out["result"].(string); !strings.Contains(res, "did not appear") {
			t.Errorf("result = %v", out["result"])
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("took %s", d)
		}
	})
}

func TestTorrentAddRejected(t *testing.T) {
	hash := hex.EncodeToString(randomHash(t))
	forEachDatasetWith(t, func(t *testing.T, ds *dataset) {
		if ds.addsListIDs(t) {
			capturePost(t, "/api/v2/torrents/add", 409, "")
		} else {
			capturePost(t, "/api/v2/torrents/add", 200, "Fails.")
		}
	}, func(t *testing.T, ds *dataset, c *rpcClient) {
		out := c.call("torrent-add", map[string]interface{}{"filename": "magnet:?xt=urn:btih:" + hash})
		if res, _ := out["result"].(string); !strings.Contains(res, qBT.ErrAddRejected.Error()) {
			t.Errorf("result = %v", out["result"])
		}
	})
}

func TestTorrentAddInvalid(t *testing.T) {
	var add *captured
	hash := hex.EncodeToString(randomHash(t))
	forEachDatasetWith(t, func(t *testing.T, ds *dataset) {
		add = capturePost(t, "/api/v2/torrents/add", 200, "Ok.")
	}, func(t *testing.T, ds *dataset, c *rpcClient) {
		for name, args := range map[string]map[string]interface{}{
			"nothing":                {"paused": true},
			"local path":             {"filename": "/tmp/x.torrent"},
			"bad magnet":             {"filename": "magnet:?dn=x"},
			"short btih":             {"filename": "magnet:?xt=urn:btih:abcd"},
			"bad base64":             {"metainfo": "%%%"},
			"not a torrent":          {"metainfo": base64.StdEncoding.EncodeToString([]byte("hello"))},
			"label with comma":       {"filename": "magnet:?xt=urn:btih:" + hash, "labels": []string{"a,b"}},
			"files-unwanted, magnet": {"filename": "magnet:?xt=urn:btih:" + hash, "files-unwanted": []int{0}},
			"files-unwanted, URL":    {"filename": "https://x.invalid/a.torrent", "files-unwanted": []int{0}},
			"paused string":          {"filename": "magnet:?xt=urn:btih:" + hash, "paused": "yes"},
		} {
			if out := c.call("torrent-add", args); out["result"] == "success" {
				t.Errorf("%s: expected an error", name)
			}
		}
		if n := add.count(); n != 0 {
			t.Errorf("%d torrents/add calls for invalid requests", n)
		}
	})
}

// --- parsing -----------------------------------------------------------------

func TestParseMagnet(t *testing.T) {
	raw := bytes.Repeat([]byte{0xab}, 20)
	v1 := hex.EncodeToString(raw)
	v2 := strings.Repeat("cd", 32)
	tests := []struct {
		link   string
		v1, v2 string
		name   string
	}{
		{"magnet:?xt=urn:btih:" + v1 + "&dn=n", v1, "", "n"},
		{"magnet:?xt=urn:btih:" + strings.ToUpper(v1), v1, "", ""},
		{"magnet:?xt=urn:btih:" + base32.StdEncoding.EncodeToString(raw), v1, "", ""},
		{"magnet:?xt=urn:btih:" + strings.ToLower(base32.StdEncoding.EncodeToString(raw)), v1, "", ""},
		{"magnet:?xt=urn:btmh:1220" + v2, "", v2, ""},
		{"magnet:?xt=urn:btih:" + v1 + "&xt=urn:btmh:1220" + strings.ToUpper(v2) + "&dn=hybrid", v1, v2, "hybrid"},
	}
	for _, tt := range tests {
		h, name, err := parseMagnet(tt.link)
		if err != nil || h.v1 != tt.v1 || h.v2 != tt.v2 || name != tt.name {
			t.Errorf("parseMagnet(%s) = %+v %q %v", tt.link, h, name, err)
		}
	}
	for _, bad := range []string{
		"magnet:?dn=x", "magnet:?xt=urn:btih:zz" + v1[2:], "magnet:?xt=urn:btih:" + v1[:39],
		"magnet:?xt=urn:btmh:1114" + v2, "magnet:?xt=urn:sha1:" + v1,
	} {
		if _, _, err := parseMagnet(bad); err == nil {
			t.Errorf("parseMagnet(%s): expected error", bad)
		}
	}
}

func TestParseMetainfoHashes(t *testing.T) {
	torrent, want := makeTorrent("n", 1, 2)
	v1, v2, name, err := ParseMetainfo(torrent)
	if err != nil || v1 != want || v2 != "" || name != "n" {
		t.Errorf("v1 torrent: %s %q %q %v, want %s", v1, v2, name, err, want)
	}

	// Hybrid (BEP 52): meta version 2 plus v1 pieces; v2 is SHA-256 of the same bytes.
	info := "d9:file treed1:xd0:d6:lengthi1e11:pieces root32:" + strings.Repeat("\x02", 32) + "eee" +
		"6:lengthi1e12:meta versioni2e4:name1:x12:piece lengthi16384e6:pieces20:" + strings.Repeat("\x03", 20) + "e"
	torrent = []byte("d4:info" + info + "e")
	s1, s2 := sha1.Sum([]byte(info)), sha256.Sum256([]byte(info))
	v1, v2, name, err = ParseMetainfo(torrent)
	if err != nil || v1 != hex.EncodeToString(s1[:]) || v2 != hex.EncodeToString(s2[:]) || name != "x" {
		t.Errorf("hybrid torrent: %s %s %q %v", v1, v2, name, err)
	}

	for _, bad := range []string{"", "hello", "d8:announce1:xe", "li1ee"} {
		if _, _, _, err := ParseMetainfo([]byte(bad)); err == nil {
			t.Errorf("ParseMetainfo(%q): expected error", bad)
		}
	}
}
