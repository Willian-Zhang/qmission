package qBT

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"gopkg.in/h2non/gock.v1"
)

// formBody is the urlencoded body Go's url.Values.Encode produces, as a gock regex.
func formBody(v url.Values) string {
	return "^" + strings.NewReplacer("|", `\|`, ".", `\.`, "+", `\+`, "?", `\?`).Replace(v.Encode()) + "$"
}

// Every endpoint and its form fields, per qBT version (names and params are the
// same in 4.6.4 and 5.3.0, torrentscontroller.cpp).
func TestPathAndQueueCallsByVersion(t *testing.T) {
	for _, v := range []struct{ webapi, app string }{{"2.9.3", "v4.6.4"}, {"2.16.2", "v5.3.0"}} {
		t.Run(v.webapi, func(t *testing.T) {
			defer gock.Off()
			mockLogin("SID=1")
			mockVersion(v.webapi, v.app)
			expect := func(path string, form url.Values) {
				gock.New(testAddr).Post("/api/v2/" + path + "$").MatchType("url").BodyString(formBody(form)).Reply(200)
			}
			both := hashA + "|" + hashB
			expect("torrents/setLocation", url.Values{"hashes": {both}, "location": {"/data/new dir"}})
			expect("torrents/rename", url.Values{"hash": {hashA}, "name": {"New name"}})
			expect("torrents/renameFile", url.Values{"hash": {hashA}, "oldPath": {"Top/a.mkv"}, "newPath": {"Top/b.mkv"}})
			expect("torrents/renameFolder", url.Values{"hash": {hashA}, "oldPath": {"Top"}, "newPath": {"New top"}})
			expect("torrents/setForceStart", url.Values{"hashes": {both}, "value": {"true"}})
			for _, m := range []QueueMove{QueueTop, QueueBottom, QueueUp, QueueDown} {
				expect(string(m), url.Values{"hashes": {both}})
			}

			q := newTestConn(t, true)
			check := func(what string, err error) {
				if err != nil {
					t.Errorf("%s: %v", what, err)
				}
			}
			check("SetLocation", q.SetLocation([]Hash{hashA, hashB}, "/data/new dir"))
			check("RenameTorrent", q.RenameTorrent(hashA, "New name"))
			check("RenameFile", q.RenameFile(hashA, "Top/a.mkv", "Top/b.mkv"))
			check("RenameFolder", q.RenameFolder(hashA, "Top", "New top"))
			check("SetForceStart", q.SetForceStart([]Hash{hashA, hashB}, true))
			for _, m := range []QueueMove{QueueTop, QueueBottom, QueueUp, QueueDown} {
				check(string(m), q.MoveInQueue(m, []Hash{hashA, hashB}))
			}
			if n := pendingOnce(); n != 0 {
				t.Errorf("pending mocks: %d", n)
			}
		})
	}
}

// qBT's error answers (4.6.4 and 5.3.0 torrentscontroller.cpp) map to sentinels,
// without the qBT message (it quotes paths).
func TestPathAndQueueErrors(t *testing.T) {
	defer gock.Off()
	mockLogin("SID=1")
	mockVersion("2.16.2", "v5.3.0")
	reply := func(path string, status int, body string) {
		gock.New(testAddr).Post("/api/v2/" + path + "$").Reply(status).BodyString(body)
	}
	reply("torrents/setLocation", 400, "Save path cannot be empty")
	reply("torrents/setLocation", 409, "Cannot make save path")
	reply("torrents/rename", 409, "Incorrect torrent name")
	reply("torrents/rename", 404, "")
	reply("torrents/renameFile", 409, "The file already exists: 'secret/path'.")
	reply("torrents/renameFolder", 409, "No such folder: 'secret/path'.")
	reply("torrents/topPrio", 409, "Torrent queueing must be enabled")
	reply("torrents/setForceStart", 500, "boom")

	q := newTestConn(t, true)
	tests := []struct {
		name string
		err  error
		want error
	}{
		{"setLocation 400", q.SetLocation([]Hash{hashA}, " "), ErrLocationEmpty},
		{"setLocation 409", q.SetLocation([]Hash{hashA}, "/x"), ErrCannotCreateLocation},
		{"rename 409", q.RenameTorrent(hashA, " "), ErrInvalidName},
		{"rename 404", q.RenameTorrent(hashA, "x"), ErrTorrentNotFound},
		{"renameFile 409", q.RenameFile(hashA, "a", "b"), ErrRenameRejected},
		{"renameFolder 409", q.RenameFolder(hashA, "a", "b"), ErrRenameRejected},
		{"topPrio 409", q.MoveInQueue(QueueTop, []Hash{hashA}), ErrQueueingDisabled},
	}
	for _, tt := range tests {
		if !errors.Is(tt.err, tt.want) {
			t.Errorf("%s: err = %v, want %v", tt.name, tt.err, tt.want)
		} else if strings.Contains(tt.err.Error(), "secret") {
			t.Errorf("%s: error quotes qBT's message: %v", tt.name, tt.err)
		}
	}
	var httpErr *HTTPError
	if err := q.SetForceStart([]Hash{hashA}, true); !errors.As(err, &httpErr) || httpErr.StatusCode != 500 {
		t.Errorf("setForceStart 500: err = %v", err)
	}
	if n := pendingOnce(); n != 0 {
		t.Errorf("pending mocks: %d", n)
	}
}

// renameFile/renameFolder with oldPath/newPath arrived in WebAPI 2.7 (qBT
// 4.3.3); older versions get ErrRenameUnsupported without a call, while
// torrents/rename (WebAPI 2.0) still works.
func TestRenamePathVersionGate(t *testing.T) {
	for _, v := range []struct {
		webapi string
		ok     bool
	}{{"2.6.2", false}, {"2.7", true}, {"2.9.3", true}} {
		t.Run(v.webapi, func(t *testing.T) {
			defer gock.Off()
			mockLogin("SID=1")
			mockVersion(v.webapi, "v4.x")
			gock.New(testAddr).Post("/api/v2/torrents/rename$").Reply(200)
			if v.ok {
				gock.New(testAddr).Post("/api/v2/torrents/renameFile").Reply(200)
				gock.New(testAddr).Post("/api/v2/torrents/renameFolder").Reply(200)
			}
			q := newTestConn(t, true)
			for name, err := range map[string]error{
				"renameFile":   q.RenameFile(hashA, "a", "b"),
				"renameFolder": q.RenameFolder(hashA, "a", "b"),
			} {
				if got := errors.Is(err, ErrRenameUnsupported); got == v.ok || (v.ok && err != nil) {
					t.Errorf("%s: err = %v", name, err)
				}
			}
			if err := q.RenameTorrent(hashA, "x"); err != nil {
				t.Errorf("rename: %v", err)
			}
			if n := pendingOnce(); n != 0 {
				t.Errorf("pending mocks: %d", n)
			}
		})
	}
}
