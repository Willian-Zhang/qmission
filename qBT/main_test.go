package qBT

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"gopkg.in/h2non/gock.v1"
)

const testAddr = "http://qbt.test:8080"

// Synthetic hashes; no real torrent data in these tests.
const (
	hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	hashC = "cccccccccccccccccccccccccccccccccccccccc"
)

func mockLogin(cookie string) {
	gock.New(testAddr).
		Post("/api/v2/auth/login").
		MatchType("url").
		BodyString("^password=pass&username=user$").
		Reply(200).
		SetHeader("Set-Cookie", cookie+"; HttpOnly; path=/").
		BodyString("Ok.")
}

func mockVersion(webapi, app string) {
	gock.New(testAddr).Get("/api/v2/app/webapiVersion").Persist().Reply(200).BodyString(webapi)
	gock.New(testAddr).Get("/api/v2/app/version").Persist().Reply(200).BodyString(app)
}

// pendingOnce counts unused non-persistent mocks.
func pendingOnce() (n int) {
	for _, m := range gock.Pending() {
		if !m.Request().Persisted {
			n++
		}
	}
	return
}

func newTestConn(t *testing.T, useSync bool) *Connection {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{}}
	gock.InterceptClient(client)
	q := &Connection{}
	if err := q.Init(testAddr+"/", client, useSync); err != nil {
		t.Fatal(err)
	}
	q.SetCredentials("user", "pass")
	return q
}

func TestParseAPIVersion(t *testing.T) {
	tests := []struct {
		in   string
		want APIVersion
	}{
		{"2.9.3", APIVersion{2, 9, 3}},
		{"2.16.2", APIVersion{2, 16, 2}},
		{"2.11", APIVersion{2, 11, 0}}, // qBT drops trailing zero components
		{"2.0\n", APIVersion{2, 0, 0}},
	}
	for _, tt := range tests {
		got, err := ParseAPIVersion(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("ParseAPIVersion(%q) = %v, %v; want %v", tt.in, got, err, tt.want)
		}
	}
	for _, bad := range []string{"", "v2.9", "2.x", "1.2.3.4", "Forbidden"} {
		if _, err := ParseAPIVersion(bad); err == nil {
			t.Errorf("ParseAPIVersion(%q): expected error", bad)
		}
	}
}

func TestGatesFor(t *testing.T) {
	v4, _ := ParseAPIVersion("2.9.3") // qBT 4.6.4
	g4 := GatesFor(v4)
	if g4.StopStart || g4.StopEndpoint != "torrents/pause" || g4.StartEndpoint != "torrents/resume" ||
		g4.StoppedDL != "pausedDL" || g4.StoppedUP != "pausedUP" ||
		g4.AddStoppedPref != "start_paused_enabled" || g4.AddStoppedParam != "paused" {
		t.Errorf("2.9.3 gates: %+v", g4)
	}
	v5, _ := ParseAPIVersion("2.16.2") // qBT 5.3.0
	g5 := GatesFor(v5)
	if !g5.StopStart || g5.StopEndpoint != "torrents/stop" || g5.StartEndpoint != "torrents/start" ||
		g5.StoppedDL != "stoppedDL" || g5.StoppedUP != "stoppedUP" ||
		g5.AddStoppedPref != "add_stopped_enabled" || g5.AddStoppedParam != "stopped" {
		t.Errorf("2.16.2 gates: %+v", g5)
	}
	if !GatesFor(APIVersion{2, 11, 0}).StopStart || GatesFor(APIVersion{2, 10, 9}).StopStart {
		t.Error("gate boundary is not 2.11.0")
	}
	for _, s := range []string{"pausedDL", "pausedUP", "stoppedDL", "stoppedUP"} {
		if !IsStoppedState(s) {
			t.Errorf("IsStoppedState(%s) = false", s)
		}
	}
	if IsStoppedState("stalledDL") {
		t.Error("IsStoppedState(stalledDL) = true")
	}
}

func TestStopStartEndpointByVersion(t *testing.T) {
	tests := []struct {
		webapi, app, stop, start string
	}{
		{"2.9.3", "v4.6.4", "/api/v2/torrents/pause", "/api/v2/torrents/resume"},
		{"2.16.2", "v5.3.0", "/api/v2/torrents/stop", "/api/v2/torrents/start"},
	}
	for _, tt := range tests {
		t.Run(tt.webapi, func(t *testing.T) {
			defer gock.Off()
			mockLogin("SID=1")
			mockVersion(tt.webapi, tt.app)
			gock.New(testAddr).Post(tt.stop).MatchType("url").
				BodyString("^hashes=" + hashA + "%7C" + hashB + "$").Reply(200)
			gock.New(testAddr).Post(tt.start).MatchType("url").
				BodyString("^hashes=" + hashA + "$").Reply(200)

			q := newTestConn(t, true)
			if err := q.StopTorrents([]Hash{hashA, hashB}); err != nil {
				t.Fatal(err)
			}
			if err := q.StartTorrents([]Hash{hashA}); err != nil {
				t.Fatal(err)
			}
			if got := q.APIVersion().String(); got != tt.webapi {
				t.Errorf("APIVersion = %s", got)
			}
			if got := q.AppVersion(); got != tt.app {
				t.Errorf("AppVersion = %s", got)
			}
			if n := pendingOnce(); n != 0 {
				t.Errorf("pending mocks: %d", n)
			}
		})
	}
}

func TestLoginErrors(t *testing.T) {
	t.Run("Fails. body", func(t *testing.T) {
		defer gock.Off()
		gock.New(testAddr).Post("/api/v2/auth/login").Times(1).Reply(200).BodyString("Fails.")
		q := newTestConn(t, true)
		if err := q.Login(); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("got %v", err)
		}
		// Sticky: no second attempt against qBT (would count towards the ban).
		if err := q.Login(); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("got %v", err)
		}
		if q.IsLoggedIn() {
			t.Error("logged in after failure")
		}
	})
	t.Run("401 (WebAPI >= 2.14)", func(t *testing.T) {
		defer gock.Off()
		gock.New(testAddr).Post("/api/v2/auth/login").Reply(401)
		q := newTestConn(t, true)
		if err := q.Login(); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("banned", func(t *testing.T) {
		defer gock.Off()
		gock.New(testAddr).Post("/api/v2/auth/login").Reply(403).
			BodyString("Your IP address has been banned after too many failed authentication attempts.")
		q := newTestConn(t, true)
		if err := q.Login(); !errors.Is(err, ErrBanned) {
			t.Fatalf("got %v", err)
		}
		if errors.Is(q.Login(), ErrInvalidCredentials) {
			t.Error("banned reported as invalid credentials")
		}
	})
	t.Run("no webapiVersion (< 4.1)", func(t *testing.T) {
		defer gock.Off()
		mockLogin("SID=1")
		gock.New(testAddr).Get("/api/v2/app/webapiVersion").Reply(404)
		q := newTestConn(t, true)
		if err := q.Login(); !errors.Is(err, ErrUnsupportedVersion) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("WebAPI 1.x", func(t *testing.T) {
		defer gock.Off()
		mockLogin("SID=1")
		mockVersion("1.9", "v4.0.4")
		q := newTestConn(t, true)
		if err := q.Login(); !errors.Is(err, ErrUnsupportedVersion) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("no auth/login (< 4.1)", func(t *testing.T) {
		defer gock.Off()
		gock.New(testAddr).Post("/api/v2/auth/login").Reply(404)
		q := newTestConn(t, true)
		if err := q.Login(); !errors.Is(err, ErrUnsupportedVersion) {
			t.Fatalf("got %v", err)
		}
	})
}

// qBT 5.x names the session cookie QBT_SID_<port>; 4.x allows a custom name.
func TestLoginCustomCookieName(t *testing.T) {
	defer gock.Off()
	mockLogin("QBT_SID_8080=abc")
	mockVersion("2.16.2", "v5.3.0")
	gock.New(testAddr).Get("/api/v2/app/preferences").MatchHeader("Cookie", "^QBT_SID_8080=abc$").
		Reply(200).BodyString(`{"proxy_type":"None"}`)
	q := newTestConn(t, true)
	if _, err := q.GetPreferences(); err != nil {
		t.Fatal(err)
	}
}

func TestReloginOn403(t *testing.T) {
	defer gock.Off()
	mockLogin("SID=1")
	mockLogin("SID=2")
	mockVersion("2.9.3", "v4.6.4")
	gock.New(testAddr).Get("/api/v2/sync/maindata").MatchHeader("Cookie", "^SID=1$").
		Reply(403).BodyString("Forbidden")
	gock.New(testAddr).Get("/api/v2/sync/maindata").MatchHeader("Cookie", "^SID=2$").
		Reply(200).BodyString(`{"rid":1,"full_update":true,"torrents":{"` + hashA + `":{"name":"a"}}}`)

	q := newTestConn(t, true)
	if err := q.UpdateTorrentsList(); err != nil {
		t.Fatal(err)
	}
	if q.TorrentsList.ItemsNum() != 1 {
		t.Errorf("ItemsNum = %d", q.TorrentsList.ItemsNum())
	}
	if n := pendingOnce(); n != 0 {
		t.Errorf("pending mocks: %d", n)
	}
}

func TestStill403AfterRelogin(t *testing.T) {
	defer gock.Off()
	mockLogin("SID=1")
	mockLogin("SID=2")
	gock.New(testAddr).Post("/api/v2/auth/login").Reply(500) // a third login must not happen
	mockVersion("2.9.3", "v4.6.4")
	gock.New(testAddr).Get("/api/v2/app/preferences").Persist().Reply(403)

	q := newTestConn(t, true)
	if _, err := q.GetPreferences(); !errors.Is(err, ErrForbidden) {
		t.Fatalf("got %v", err)
	}
}

func TestHTTPErrorIsReturned(t *testing.T) {
	defer gock.Off()
	mockLogin("SID=1")
	mockVersion("2.16.2", "v5.3.0")
	gock.New(testAddr).Post("/api/v2/torrents/filePrio").Reply(409).BodyString("File ID is not valid")
	q := newTestConn(t, true)
	_, err := q.PostForm("torrents/filePrio", nil)
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != 409 {
		t.Fatalf("got %v", err)
	}
}

func syncMock(rid int, body string) {
	gock.New(testAddr).Get("/api/v2/sync/maindata").MatchParam("rid", fmt.Sprint(rid)).
		Reply(200).BodyString(body)
}

func TestSyncFullUpdateAndIDs(t *testing.T) {
	defer gock.Off()
	mockLogin("SID=1")
	mockVersion("2.16.2", "v5.3.0")
	syncMock(0, `{"rid":1,"full_update":true,
		"torrents":{"`+hashA+`":{"name":"a","state":"stoppedDL","dlspeed":5},"`+hashB+`":{"name":"b"}},
		"server_state":{"alltime_dl":100,"alltime_ul":200,"free_space_on_disk":300,"connection_status":"connected","dl_info_speed":7}}`)
	syncMock(1, `{"rid":2,
		"torrents":{"`+hashA+`":{"dlspeed":9},"`+hashC+`":{"name":"c"}},
		"server_state":{"alltime_dl":150}}`)
	// After a qBT restart (or a new qBT session) the rid is unknown: full update without A.
	syncMock(2, `{"rid":3,"full_update":true,
		"torrents":{"`+hashC+`":{"name":"c2"},"`+hashB+`":{"name":"b2"}},
		"server_state":{"alltime_dl":10,"connection_status":"firewalled"}}`)
	syncMock(3, `{"rid":4,"torrents":{"`+hashA+`":{"name":"a again"}}}`)

	q := newTestConn(t, true)
	list := &q.TorrentsList

	if err := q.UpdateTorrentsList(); err != nil {
		t.Fatal(err)
	}
	a, b := list.ByHash(hashA), list.ByHash(hashB)
	if a == nil || b == nil || a.Id != 1 || b.Id != 2 {
		t.Fatalf("IDs must start at 1 in qBT order: a=%+v b=%+v", a, b)
	}
	if list.ByID(0) != nil {
		t.Error("ID 0 must not exist")
	}
	ss := list.ServerState()
	if ss.Alltime_dl != 100 || ss.Alltime_ul != 200 || ss.Free_space_on_disk != 300 ||
		ss.Connection_status != "connected" || ss.Dl_info_speed != 7 {
		t.Errorf("server_state: %+v", ss)
	}

	// Delta: merge into A, add C, merge server_state.
	if err := q.UpdateTorrentsList(); err != nil {
		t.Fatal(err)
	}
	a2 := list.ByID(1)
	if a2.Dlspeed != 9 || a2.Name != "a" || a2.State != "stoppedDL" {
		t.Errorf("delta not merged: %+v", a2)
	}
	if a.Dlspeed != 5 {
		t.Error("an old *TorrentInfo was modified in place")
	}
	if c := list.ByHash(hashC); c == nil || c.Id != 3 {
		t.Fatalf("C: %+v", c)
	}
	ss = list.ServerState()
	if ss.Alltime_dl != 150 || ss.Alltime_ul != 200 || ss.Free_space_on_disk != 300 {
		t.Errorf("server_state not merged: %+v", ss)
	}

	// Full update: table and server_state replaced, A gone, B/C keep their IDs.
	if err := q.UpdateTorrentsList(); err != nil {
		t.Fatal(err)
	}
	if list.ByHash(hashA) != nil || list.ByID(1) != nil || list.ItemsNum() != 2 {
		t.Errorf("A still present after full_update (items %d)", list.ItemsNum())
	}
	if b := list.ByID(2); b == nil || b.Hash != hashB || b.Name != "b2" {
		t.Errorf("B: %+v", b)
	}
	if c := list.ByID(3); c == nil || c.Hash != hashC || c.Name != "c2" {
		t.Errorf("C: %+v", c)
	}
	if removed := list.GetRemoved(); len(removed) != 1 || removed[0] != 1 {
		t.Errorf("removed = %v", removed)
	}
	ss = list.ServerState()
	if ss.Alltime_dl != 10 || ss.Alltime_ul != 0 || ss.Connection_status != "firewalled" {
		t.Errorf("server_state not replaced: %+v", ss)
	}
	if ids := list.Slice(); len(ids) != 2 || ids[0].Id != 2 || ids[1].Id != 3 {
		t.Errorf("Slice order: %v", ids)
	}

	// A comes back: same ID, no longer reported as removed.
	if err := q.UpdateTorrentsList(); err != nil {
		t.Fatal(err)
	}
	if a := list.ByHash(hashA); a == nil || a.Id != 1 {
		t.Errorf("A did not get its old ID back: %+v", a)
	}
	if removed := list.GetRemoved(); len(removed) != 0 {
		t.Errorf("removed = %v", removed)
	}
}

func TestDirectListIDs(t *testing.T) {
	defer gock.Off()
	mockLogin("SID=1")
	mockVersion("2.9.3", "v4.6.4")
	gock.New(testAddr).Get("/api/v2/torrents/info").Reply(200).
		BodyString(`[{"hash":"` + hashB + `","name":"b"},{"hash":"` + hashA + `","name":"a"}]`)
	gock.New(testAddr).Get("/api/v2/torrents/info").Reply(200).
		BodyString(`[{"hash":"` + hashA + `","name":"a"}]`)

	q := newTestConn(t, false)
	list := &q.TorrentsList
	if err := q.UpdateTorrentsList(); err != nil {
		t.Fatal(err)
	}
	if list.ByHash(hashB).Id != 1 || list.ByHash(hashA).Id != 2 {
		t.Errorf("IDs: b=%d a=%d", list.ByHash(hashB).Id, list.ByHash(hashA).Id)
	}
	if err := q.UpdateTorrentsList(); err != nil {
		t.Fatal(err)
	}
	if list.ByHash(hashB) != nil || list.ByID(2) == nil {
		t.Error("table not replaced")
	}
	if removed := list.GetRemoved(); len(removed) != 1 || removed[0] != 1 {
		t.Errorf("removed = %v", removed)
	}
}

func TestSyncDecodeErrorKeepsTable(t *testing.T) {
	defer gock.Off()
	mockLogin("SID=1")
	mockVersion("2.9.3", "v4.6.4")
	syncMock(0, `{"rid":1,"full_update":true,"torrents":{"`+hashA+`":{"name":"a"}}}`)
	syncMock(1, `{"rid":2,"torrents":{"`+hashA+`":{"name":42}}}`)
	q := newTestConn(t, true)
	if err := q.UpdateTorrentsList(); err != nil {
		t.Fatal(err)
	}
	if err := q.UpdateTorrentsList(); err == nil {
		t.Fatal("expected a decode error")
	}
	if a := q.TorrentsList.ByID(1); a == nil || a.Name != "a" {
		t.Errorf("table changed by a bad payload: %+v", a)
	}
}

// Run with -race: updates and reads from concurrent handlers.
func TestConcurrentAccess(t *testing.T) {
	defer gock.Off()
	mockLogin("SID=1")
	mockVersion("2.16.2", "v5.3.0")
	gock.New(testAddr).Get("/api/v2/sync/maindata").Persist().Reply(200).
		BodyString(`{"rid":1,"full_update":true,"torrents":{"` + hashA + `":{"name":"a"},"` + hashB + `":{"name":"b"}},"server_state":{"alltime_dl":1}}`)
	q := newTestConn(t, true)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if err := q.UpdateTorrentsList(); err != nil {
					t.Error(err)
					return
				}
				for _, item := range q.TorrentsList.AllItems() {
					_ = item.Name
				}
				_ = q.TorrentsList.Slice()
				_ = q.TorrentsList.GetActive()
				_ = q.TorrentsList.GetRemoved()
				_ = q.TorrentsList.ByID(1)
				_ = q.TorrentsList.ServerState()
				_ = q.Gates()
			}
		}()
	}
	wg.Wait()
}
