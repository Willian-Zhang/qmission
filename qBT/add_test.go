package qBT

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"testing"

	"gopkg.in/h2non/gock.v1"
)

// matcherWith is gock's default matchers plus fn, as a private matcher: gock
// v1.0.14's AddMatcher appends to the shared DefaultMatcher, affecting every mock.
func matcherWith(fn gock.MatchFunc) gock.Matcher {
	m := gock.NewEmptyMatcher()
	m.Matchers = append(append([]gock.MatchFunc{}, gock.Matchers...), fn)
	return m
}

// multipartFields parses a multipart request body into its text fields and
// file parts (by field name), restoring the body for gock.
func multipartFields(t *testing.T, req *http.Request) (map[string]string, map[string][]byte) {
	body, _ := io.ReadAll(req.Body)
	req.Body = io.NopCloser(bytes.NewReader(body))
	_, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil {
		t.Errorf("content type: %v", err)
		return nil, nil
	}
	fields, files := map[string]string{}, map[string][]byte{}
	r := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		part, err := r.NextPart()
		if err != nil {
			break
		}
		data, _ := io.ReadAll(part)
		if part.FileName() != "" {
			files[part.FormName()] = data
		} else {
			fields[part.FormName()] = string(data)
		}
	}
	return fields, files
}

func TestAddTorrentFieldsByVersion(t *testing.T) {
	tests := []struct {
		webapi, app, stoppedParam, reply string
	}{
		{"2.9.3", "v4.6.4", "paused", "Ok."},
		{"2.16.2", "v5.3.0", "stopped", `{"success_count":1,"failure_count":0,"pending_count":0,"added_torrent_ids":["` + hashA + `"]}`},
	}
	for _, tt := range tests {
		t.Run(tt.webapi, func(t *testing.T) {
			defer gock.Off()
			mockLogin("SID=1")
			mockVersion(tt.webapi, tt.app)
			var fields map[string]string
			var files map[string][]byte
			gock.New(testAddr).Post("/api/v2/torrents/add").
				SetMatcher(matcherWith(func(req *http.Request, _ *gock.Request) (bool, error) {
					fields, files = multipartFields(t, req)
					return true, nil
				})).Reply(200).BodyString(tt.reply)

			q := newTestConn(t, true)
			stopped := true
			res, err := q.AddTorrent(AddParams{
				Torrent: []byte("d4:infod4:name1:xee"), SavePath: "/dl", Tags: []string{"a", "b c"},
				Stopped: &stopped, Sequential: true, FirstLastPiecePrio: true, Cookie: "k=v",
			})
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"savepath": "/dl", "tags": "a,b c", tt.stoppedParam: "true",
				"sequentialDownload": "true", "firstLastPiecePrio": "true", "cookie": "k=v"}
			for k, v := range want {
				if fields[k] != v {
					t.Errorf("field %s = %q, want %q", k, fields[k], v)
				}
			}
			if len(fields) != len(want) {
				t.Errorf("fields = %v", fields)
			}
			if string(files["torrents"]) != "d4:infod4:name1:xee" {
				t.Errorf("torrents part = %q", files["torrents"])
			}
			if tt.webapi == "2.16.2" && (len(res.AddedIDs) != 1 || res.AddedIDs[0] != hashA) {
				t.Errorf("AddedIDs = %v", res.AddedIDs)
			}
		})
	}
}

func TestAddTorrentMinimalFields(t *testing.T) {
	defer gock.Off()
	mockLogin("SID=1")
	mockVersion("2.16.2", "v5.3.0")
	var fields map[string]string
	gock.New(testAddr).Post("/api/v2/torrents/add").
		SetMatcher(matcherWith(func(req *http.Request, _ *gock.Request) (bool, error) {
			fields, _ = multipartFields(t, req)
			return true, nil
		})).Reply(202).BodyString(`{"success_count":0,"failure_count":0,"pending_count":1,"added_torrent_ids":[]}`)
	q := newTestConn(t, true)
	res, err := q.AddTorrent(AddParams{URLs: []string{"https://example.invalid/x.torrent"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pending != 1 || len(res.AddedIDs) != 0 {
		t.Errorf("result = %+v", res)
	}
	// Unset options are left to qBT's defaults (in particular no stopped=false).
	if len(fields) != 1 || fields["urls"] != "https://example.invalid/x.torrent" {
		t.Errorf("fields = %v", fields)
	}
}

func TestAddTorrentRejected(t *testing.T) {
	tests := []struct {
		name, webapi string
		status       int
		body         string
	}{
		{"4.x Fails.", "2.9.3", 200, "Fails."},
		{"5.x 409", "2.16.2", 409, ""},
		{"5.x nothing added", "2.16.2", 200, `{"success_count":0,"failure_count":1,"pending_count":0,"added_torrent_ids":[]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer gock.Off()
			mockLogin("SID=1")
			mockVersion(tt.webapi, "v")
			gock.New(testAddr).Post("/api/v2/torrents/add").Reply(tt.status).BodyString(tt.body)
			q := newTestConn(t, true)
			if _, err := q.AddTorrent(AddParams{URLs: []string{"magnet:?xt=urn:btih:" + hashA}}); !errors.Is(err, ErrAddRejected) {
				t.Errorf("err = %v, want ErrAddRejected", err)
			}
		})
	}
	t.Run("500", func(t *testing.T) {
		defer gock.Off()
		mockLogin("SID=1")
		mockVersion("2.16.2", "v")
		gock.New(testAddr).Post("/api/v2/torrents/add").Reply(500).BodyString("x")
		q := newTestConn(t, true)
		var httpErr *HTTPError
		if _, err := q.AddTorrent(AddParams{URLs: []string{"magnet:?xt=urn:btih:" + hashA}}); !errors.As(err, &httpErr) {
			t.Errorf("err = %v, want HTTPError", err)
		}
	})
}

func TestSetFilePriorityAndReannounce(t *testing.T) {
	defer gock.Off()
	mockLogin("SID=1")
	mockVersion("2.9.3", "v4.6.4")
	gock.New(testAddr).Post("/api/v2/torrents/filePrio").MatchType("url").
		BodyString("^hash=" + hashA + "&id=0%7C2%7C5&priority=0$").Reply(200)
	gock.New(testAddr).Post("/api/v2/torrents/reannounce").MatchType("url").
		BodyString("^hashes=" + hashA + "%7C" + hashB + "$").Reply(200)
	q := newTestConn(t, true)
	if err := q.SetFilePriority(hashA, []int{0, 2, 5}, 0); err != nil {
		t.Fatal(err)
	}
	if err := q.ReannounceTorrents([]Hash{hashA, hashB}); err != nil {
		t.Fatal(err)
	}
	if n := pendingOnce(); n != 0 {
		t.Errorf("pending mocks: %d", n)
	}
}

func TestFindByInfoHash(t *testing.T) {
	v2 := "dddddddddddddddddddddddddddddddddddddddd" + "eeeeeeeeeeeeeeeeeeeeeeee"
	v2Only := "ffffffffffffffffffffffffffffffffffffffff" + "000000000000000000000000"
	torrents, _ := json.Marshal(map[string]interface{}{
		hashA:       map[string]interface{}{"name": "v1", "infohash_v1": hashA},
		v2[:40]:     map[string]interface{}{"name": "hybrid (lt2 key)", "infohash_v1": hashB, "infohash_v2": v2},
		v2Only[:40]: map[string]interface{}{"name": "v2 only", "infohash_v2": v2Only},
	})
	raw := json.RawMessage(torrents)
	var list TorrentsList
	list.reset(true)
	if err := list.applyMainData(&MainData{Rid: 1, Full_update: true, Torrents: &raw}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		v1, v2, want string
	}{
		{hashA, "", "v1"},
		{"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "", "v1"},
		{hashB, "", "hybrid (lt2 key)"}, // btih-only magnet of a hybrid torrent
		{"", v2, "hybrid (lt2 key)"},
		{"", v2Only, "v2 only"},
		{hashC, "", ""},
		{"", "", ""},
	}
	for _, tt := range tests {
		got := ""
		if tor := list.FindByInfoHash(tt.v1, tt.v2); tor != nil {
			got = tor.Name
		}
		if got != tt.want {
			t.Errorf("FindByInfoHash(%q, %q) = %q, want %q", tt.v1, tt.v2, got, tt.want)
		}
	}
}
