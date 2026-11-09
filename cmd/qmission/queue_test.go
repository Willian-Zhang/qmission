package main

import (
	"reflect"
	"strings"
	"testing"
)

var queueEndpoints = map[string]string{
	"queue-move-top":    "/api/v2/torrents/topPrio",
	"queue-move-bottom": "/api/v2/torrents/bottomPrio",
	"queue-move-up":     "/api/v2/torrents/increasePrio",
	"queue-move-down":   "/api/v2/torrents/decreasePrio",
}

// TestDatasetQueueMoves: each queue-move-* is one qBT call with the selected
// hashes: one id (unknown ids skipped), all (no ids), nothing for unknown/empty ids.
func TestDatasetQueueMoves(t *testing.T) {
	caps := map[string]*captured{}
	forEachDatasetWith(t, func(t *testing.T, ds *dataset) {
		for method, path := range queueEndpoints {
			caps[method] = capturePost(t, path+"$", 200, "")
		}
	}, func(t *testing.T, ds *dataset, c *rpcClient) {
		hashes, byID := drainSync(t, ds, c)
		first := firstID(byID)
		for method := range queueEndpoints {
			c.mustSucceed(method, map[string]interface{}{"ids": []interface{}{first, 99999}})
			c.mustSucceed(method, nil)
			c.mustSucceed(method, map[string]interface{}{"ids": []int{99999}})
			c.mustSucceed(method, map[string]interface{}{"ids": []interface{}{}})
			forms := caps[method].all()
			if len(forms) != 2 {
				t.Fatalf("%s: %d qBT calls, want 2", method, len(forms))
			}
			if got := forms[0].Get("hashes"); got != byID[first] {
				t.Errorf("%s ids [%v]: hashes %q, want %q", method, first, got, byID[first])
			}
			if got := forms[1].Get("hashes"); got != strings.Join(hashes, "|") {
				t.Errorf("%s without ids: hashes %q, want all", method, got)
			}
		}
	})
}

// qBT answers 409 "Torrent queueing must be enabled" to every queue endpoint
// when queueing is off (4.6.4 and 5.3.0 torrentscontroller.cpp).
func TestDatasetQueueDisabled(t *testing.T) {
	forEachDatasetWith(t, func(t *testing.T, ds *dataset) {
		for _, path := range queueEndpoints {
			capturePost(t, path+"$", 409, "Torrent queueing must be enabled")
		}
	}, func(t *testing.T, ds *dataset, c *rpcClient) {
		_, byID := drainSync(t, ds, c)
		for method := range queueEndpoints {
			res, _ := c.call(method, map[string]interface{}{"ids": []float64{firstID(byID)}})["result"].(string)
			if !strings.HasPrefix(res, method+": ") || !strings.Contains(res, "queueing is disabled") || !strings.Contains(res, "enable queueing in qBittorrent") {
				t.Errorf("%s: result = %q", method, res)
			}
		}
	})
}

// TestDatasetTorrentStartNow: force start for the selected torrents, all of
// them without ids (rpc-spec 3.1), nothing for unknown ids.
func TestDatasetTorrentStartNow(t *testing.T) {
	var force *captured
	forEachDatasetWith(t, func(t *testing.T, ds *dataset) {
		force = capturePost(t, "/api/v2/torrents/setForceStart$", 200, "")
	}, func(t *testing.T, ds *dataset, c *rpcClient) {
		hashes, byID := drainSync(t, ds, c)
		first := firstID(byID)
		c.mustSucceed("torrent-start-now", map[string]interface{}{"ids": []interface{}{first, 99999}})
		c.mustSucceed("torrent-start-now", nil)
		c.mustSucceed("torrent-start-now", map[string]interface{}{"ids": []int{99999}})
		c.mustSucceed("torrent-start-now", map[string]interface{}{"ids": []interface{}{}})
		forms := force.all()
		if len(forms) != 2 {
			t.Fatalf("%d setForceStart calls, want 2", len(forms))
		}
		expectForm(t, forms[0], map[string]string{"hashes": byID[first], "value": "true"})
		expectForm(t, forms[1], map[string]string{"hashes": strings.Join(hashes, "|"), "value": "true"})
	})
}

func TestQueueHandlersRegistered(t *testing.T) {
	if reflect.ValueOf(rpcMethods["torrent-start-now"]).Pointer() != reflect.ValueOf(rpcHandler(TorrentStartNow)).Pointer() {
		t.Error("torrent-start-now is not served by TorrentStartNow")
	}
	for method := range queueEndpoints {
		if rpcMethods[method] == nil {
			t.Errorf("%s not registered", method)
		}
	}
}
