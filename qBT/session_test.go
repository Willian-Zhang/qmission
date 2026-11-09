package qBT

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"gopkg.in/h2non/gock.v1"
)

func TestGetPreferencesWithKeys(t *testing.T) {
	defer gock.Off()
	mockLogin("SID=1")
	mockVersion("2.9.3", "v4.6.4")
	gock.New(testAddr).Get("/api/v2/app/preferences").Reply(200).
		BodyString(`{"dl_limit": 2048, "start_paused_enabled": true, "proxy_type": "None"}`)

	q := newTestConn(t, true)
	prefs, keys, err := q.GetPreferencesWithKeys()
	if err != nil {
		t.Fatal(err)
	}
	if prefs.Dl_limit != 2048 || !prefs.AddStopped() {
		t.Errorf("prefs = %+v", prefs)
	}
	if _, ok := keys["start_paused_enabled"]; !ok {
		t.Error("start_paused_enabled missing from keys")
	}
	if _, ok := keys["add_stopped_enabled"]; ok {
		t.Error("add_stopped_enabled reported but not sent")
	}
}

func TestSetPreferencesSendsJSONForm(t *testing.T) {
	defer gock.Off()
	mockLogin("SID=1")
	mockVersion("2.16.2", "v5.3.0")
	var got map[string]interface{}
	gock.New(testAddr).Post("/api/v2/app/setPreferences").
		SetMatcher(matcherWith(func(req *http.Request, _ *gock.Request) (bool, error) {
			body, _ := io.ReadAll(req.Body)
			form, err := url.ParseQuery(string(body))
			if err != nil {
				return false, err
			}
			if err := json.Unmarshal([]byte(form.Get("json")), &got); err != nil {
				return false, err
			}
			return req.Header.Get("Content-Type") == "application/x-www-form-urlencoded", nil
		})).Reply(200)

	q := newTestConn(t, true)
	want := map[string]interface{}{"dl_limit": 102400.0, "dht": false}
	if err := q.SetPreferences(map[string]interface{}{"dl_limit": 102400, "dht": false}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("json = %v, want %v", got, want)
	}
	if n := pendingOnce(); n != 0 {
		t.Errorf("pending mocks: %d", n)
	}
}

func TestSetAltSpeedLimitsByVersion(t *testing.T) {
	tests := []struct {
		name, webapi, app string
		on                bool
		current           string // speedLimitsMode answer for the toggle path
		wantSet           string // setSpeedLimitsMode body, "" = not called
		wantToggle        bool
	}{
		{"5.3 on", "2.16.2", "v5.3.0", true, "", "^mode=1$", false},
		{"4.6 off", "2.9.3", "v4.6.4", false, "", "^mode=0$", false},
		{"2.8.14 boundary", "2.8.14", "v4.5.0", true, "", "^mode=1$", false},
		{"4.4 toggles", "2.8.5", "v4.4.5", true, "0", "", true},
		{"4.4 already on", "2.8.5", "v4.4.5", true, "1", "", false},
		{"4.1 toggles off", "2.0", "v4.1.0", false, "1", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer gock.Off()
			mockLogin("SID=1")
			mockVersion(tt.webapi, tt.app)
			if tt.wantSet != "" {
				gock.New(testAddr).Post("/api/v2/transfer/setSpeedLimitsMode").MatchType("url").
					BodyString(tt.wantSet).Reply(200)
			}
			if tt.current != "" {
				gock.New(testAddr).Get("/api/v2/transfer/speedLimitsMode").Reply(200).BodyString(tt.current)
			}
			if tt.wantToggle {
				gock.New(testAddr).Post("/api/v2/transfer/toggleSpeedLimitsMode").Reply(200)
			}
			q := newTestConn(t, true)
			if err := q.SetAltSpeedLimits(tt.on); err != nil {
				t.Fatal(err)
			}
			if n := pendingOnce(); n != 0 {
				t.Errorf("pending mocks: %d", n)
			}
			if gock.HasUnmatchedRequest() {
				t.Errorf("unexpected requests: %d", len(gock.GetUnmatchedRequests()))
			}
		})
	}
}

func TestSpeedLimitsMode(t *testing.T) {
	defer gock.Off()
	mockLogin("SID=1")
	mockVersion("2.16.2", "v5.3.0")
	gock.New(testAddr).Get("/api/v2/transfer/speedLimitsMode").Reply(200).BodyString("1")
	gock.New(testAddr).Get("/api/v2/transfer/speedLimitsMode").Reply(200).BodyString("0")
	gock.New(testAddr).Post("/api/v2/transfer/toggleSpeedLimitsMode").Reply(200)

	q := newTestConn(t, true)
	for _, want := range []bool{true, false} {
		got, err := q.SpeedLimitsMode()
		if err != nil || got != want {
			t.Errorf("SpeedLimitsMode = %v, %v; want %v", got, err, want)
		}
	}
	if err := q.ToggleSpeedLimitsMode(); err != nil {
		t.Fatal(err)
	}
	if n := pendingOnce(); n != 0 {
		t.Errorf("pending mocks: %d", n)
	}
}
