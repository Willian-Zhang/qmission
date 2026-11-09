package main

import (
	"sort"
	"strings"
	"testing"

	"gopkg.in/h2non/gock.v1"
)

// drainSync polls until the recorded sync chain is used up, so the torrent
// table stays put, and returns its torrents' hashes in ID order plus id → hash.
func drainSync(t *testing.T, ds *dataset, c *rpcClient) ([]string, map[float64]string) {
	t.Helper()
	var list []interface{}
	for range ds.syncFiles {
		list = c.mustSucceed("torrent-get", map[string]interface{}{"fields": []string{"id", "hashString"}})["torrents"].([]interface{})
	}
	byID := map[float64]string{}
	var ids []float64
	for _, item := range list {
		tor := item.(map[string]interface{})
		id := tor["id"].(float64)
		byID[id] = tor["hashString"].(string)
		ids = append(ids, id)
	}
	sort.Float64s(ids)
	hashes := make([]string, len(ids))
	for i, id := range ids {
		hashes[i] = byID[id]
	}
	return hashes, byID
}

// TestDatasetActions: start/stop (version-gated), verify, reannounce and remove
// each make one qBT call with the selected hashes: one id, all (no ids), and
// nothing for unknown ids.
func TestDatasetActions(t *testing.T) {
	caps := map[string]*captured{}
	forEachDatasetWith(t, func(t *testing.T, ds *dataset) {
		g := ds.gates(t)
		caps["torrent-start"] = capturePost(t, "/api/v2/"+g.StartEndpoint, 200, "")
		caps["torrent-stop"] = capturePost(t, "/api/v2/"+g.StopEndpoint, 200, "")
		caps["torrent-verify"] = capturePost(t, "/api/v2/torrents/recheck", 200, "")
		caps["torrent-reannounce"] = capturePost(t, "/api/v2/torrents/reannounce", 200, "")
		caps["torrent-remove"] = capturePost(t, "/api/v2/torrents/delete", 200, "")
	}, func(t *testing.T, ds *dataset, c *rpcClient) {
		hashes, byID := drainSync(t, ds, c)
		if len(hashes) == 0 {
			t.Fatal("empty recording")
		}
		var firstID float64 = 1e9
		for id := range byID {
			if id < firstID {
				firstID = id
			}
		}
		all := strings.Join(hashes, "|")
		for _, method := range []string{"torrent-start", "torrent-stop", "torrent-verify", "torrent-reannounce", "torrent-remove"} {
			c.mustSucceed(method, map[string]interface{}{"ids": []interface{}{firstID, 99999}})
			c.mustSucceed(method, nil)
			c.mustSucceed(method, map[string]interface{}{"ids": []int{99999}})
			c.mustSucceed(method, map[string]interface{}{"ids": []interface{}{}})
			forms := caps[method].all()
			if len(forms) != 2 {
				t.Fatalf("%s: %d qBT calls, want 2", method, len(forms))
			}
			if got := forms[0].Get("hashes"); got != byID[firstID] {
				t.Errorf("%s ids [%v]: hashes %q, want %q", method, firstID, got, byID[firstID])
			}
			if got := forms[1].Get("hashes"); got != all {
				t.Errorf("%s without ids: hashes %q, want all %q", method, got, all)
			}
		}
		for _, f := range caps["torrent-remove"].all() {
			if f.Get("deleteFiles") != "false" {
				t.Errorf("torrent-remove default deleteFiles = %q", f.Get("deleteFiles"))
			}
		}
		for _, v := range []interface{}{true, 1} {
			c.mustSucceed("torrent-remove", map[string]interface{}{"ids": []interface{}{firstID}, "delete-local-data": v})
		}
		forms := caps["torrent-remove"].all()
		for _, f := range forms[2:] {
			if f.Get("deleteFiles") != "true" || f.Get("hashes") != byID[firstID] {
				t.Errorf("delete-local-data: form %v", f)
			}
		}
	})
}

func TestActionErrorBecomesResult(t *testing.T) {
	c := setUpSynthetic(t)
	gock.New(testQBTAddr).Post("/api/v2/torrents/reannounce").Reply(500).BodyString("boom")
	out := c.call("torrent-reannounce", map[string]interface{}{"ids": []int{1}})
	if res, _ := out["result"].(string); !strings.Contains(res, "HTTP 500") {
		t.Errorf("result = %v", out["result"])
	}
	if bad := c.call("torrent-verify", map[string]interface{}{"ids": map[string]int{"x": 1}}); bad["result"] == "success" {
		t.Error("object ids: expected an error")
	}
}

func TestDatasetFreeSpace(t *testing.T) {
	forEachDataset(t, func(t *testing.T, ds *dataset, c *rpcClient) {
		drainSync(t, ds, c)
		want := num(ds.serverStateAfter(len(ds.syncFiles)-1), "free_space_on_disk")
		if want <= 0 {
			t.Fatalf("recording has no free_space_on_disk")
		}
		args := c.mustSucceed("free-space", map[string]interface{}{"path": "/some/other/dir"})
		if args["path"] != "/some/other/dir" || args["size-bytes"] != want || args["total_size"] != -1.0 {
			t.Errorf("free-space = %v, want size-bytes %v", args, want)
		}
		if out := c.call("free-space", map[string]interface{}{}); out["result"] == "success" {
			t.Error("free-space without path: expected an error")
		}
	})
}

func TestFreeSpaceWithoutSync(t *testing.T) {
	quietLogs(t)
	t.Cleanup(gock.Off)
	mockTestLogin("2.16.2", "v5.3.0")
	initTestQBT(t, false)
	_, c := newTestServer(t)
	args := c.mustSucceed("free-space", map[string]interface{}{"path": "/x"})
	if args["size-bytes"] != -1.0 || args["total_size"] != -1.0 {
		t.Errorf("free-space without -sync = %v", args)
	}
}
