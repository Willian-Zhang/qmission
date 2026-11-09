package qBT

import (
	"net/url"
	"regexp"
	"testing"

	"gopkg.in/h2non/gock.v1"
)

// TestSetShareLimitsParamsByVersion: setShareLimits sends exactly the params
// each WebAPI version requires (4.5.5 = 2.8.19: ratio + seeding time; 4.6.0 =
// 2.9.2 adds inactiveSeedingTimeLimit; 2.12.0 shareLimitAction; 2.15.3 shareLimitsMode).
func TestSetShareLimitsParamsByVersion(t *testing.T) {
	limits := ShareLimits{RatioLimit: 2.5, SeedingTimeLimit: -2, InactiveSeedingTimeLimit: 30, Mode: "MatchAll"}
	tests := []struct {
		webapi string
		want   url.Values
	}{
		{"2.8.19", url.Values{"hashes": {hashA + "|" + hashB}, "ratioLimit": {"2.5"}, "seedingTimeLimit": {"-2"}}},
		{"2.9.3", url.Values{"hashes": {hashA + "|" + hashB}, "ratioLimit": {"2.5"}, "seedingTimeLimit": {"-2"},
			"inactiveSeedingTimeLimit": {"30"}}},
		{"2.12.0", url.Values{"hashes": {hashA + "|" + hashB}, "ratioLimit": {"2.5"}, "seedingTimeLimit": {"-2"},
			"inactiveSeedingTimeLimit": {"30"}, "shareLimitAction": {"Default"}}},
		{"2.16.2", url.Values{"hashes": {hashA + "|" + hashB}, "ratioLimit": {"2.5"}, "seedingTimeLimit": {"-2"},
			"inactiveSeedingTimeLimit": {"30"}, "shareLimitAction": {"Default"}, "shareLimitsMode": {"MatchAll"}}},
	}
	for _, tt := range tests {
		t.Run(tt.webapi, func(t *testing.T) {
			defer gock.Off()
			mockLogin("SID=1")
			mockVersion(tt.webapi, "v0")
			gock.New(testAddr).Post("/api/v2/torrents/setShareLimits").MatchType("url").
				BodyString("^" + regexp.QuoteMeta(tt.want.Encode()) + "$").Reply(200)
			q := newTestConn(t, true)
			if err := q.SetTorrentsShareLimits([]Hash{hashA, hashB}, limits); err != nil {
				t.Fatal(err)
			}
			if n := pendingOnce(); n != 0 {
				t.Errorf("pending mocks: %d", n)
			}
		})
	}
}

func TestTorrentSetCalls(t *testing.T) {
	defer gock.Off()
	mockLogin("SID=1")
	mockVersion("2.16.2", "v5.3.0")
	post := func(path string, form url.Values) {
		gock.New(testAddr).Post("/api/v2/torrents/" + path).MatchType("url").
			BodyString("^" + regexp.QuoteMeta(form.Encode()) + "$").Reply(200)
	}
	post("setDownloadLimit", url.Values{"hashes": {hashA}, "limit": {"102400"}})
	post("setUploadLimit", url.Values{"hashes": {hashA + "|" + hashB}, "limit": {"0"}})
	post("addTags", url.Values{"hashes": {hashA}, "tags": {"x,y"}})
	post("addTrackers", url.Values{"hash": {hashA}, "urls": {"\nudp://t.example/announce"}})
	post("removeTrackers", url.Values{"hash": {hashA}, "urls": {"http://a.example/announce|http://b.example/announce"}})
	post("editTracker", url.Values{"hash": {hashA}, "origUrl": {"http://a.example/announce"},
		"url": {"http://a.example/announce"}, "newUrl": {"http://c.example/announce"}})
	post("toggleSequentialDownload", url.Values{"hashes": {hashA}})
	post("toggleFirstLastPiecePrio", url.Values{"hashes": {hashB}})
	post("setLocation", url.Values{"hashes": {hashA}, "location": {"/data/x"}})

	q := newTestConn(t, true)
	steps := []error{
		q.SetTorrentsDownloadLimit([]Hash{hashA}, 102400),
		q.SetTorrentsUploadLimit([]Hash{hashA, hashB}, 0),
		q.AddTorrentTags([]Hash{hashA}, []string{"x", "y"}),
		q.AddTorrentTrackers(hashA, "\nudp://t.example/announce"),
		q.RemoveTorrentTrackers(hashA, []string{"http://a.example/announce", "http://b.example/announce"}),
		q.EditTorrentTracker(hashA, "http://a.example/announce", "http://c.example/announce"),
		q.ToggleSequential([]Hash{hashA}),
		q.ToggleFirstLastPiece([]Hash{hashB}),
		q.SetLocation([]Hash{hashA}, "/data/x"),
	}
	for i, err := range steps {
		if err != nil {
			t.Errorf("call %d: %v", i, err)
		}
	}
	if n := pendingOnce(); n != 0 {
		t.Errorf("pending mocks: %d", n)
	}
}
