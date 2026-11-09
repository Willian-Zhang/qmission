package main

import (
	"github.com/Willian-Zhang/qmission/qBT"
	"github.com/hekmon/transmissionrpc"
	log "github.com/sirupsen/logrus"
	"gopkg.in/h2non/gock.v1"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

var currentLogLevel = log.DebugLevel

func TestTorrentListing(t *testing.T) {
	const apiAddr = "http://localhost:8080"
	log.SetLevel(currentLogLevel)

	defer gock.Off()

	gock.Observe(gock.DumpRequest)

	gock.New(apiAddr).
		Get("/api/v2/torrents/info").
		Times(2).
		Reply(200).
		File("testdata/torrent_list.json")

	gock.New(apiAddr).
		Post("/api/v2/auth/login").
		Reply(200).
		SetHeader("Set-Cookie", "SID=1")
	setUpVersionMocks(apiAddr, "2.2", "v4.1.5")

	// 1
	setUpMocks(apiAddr, "cf7da7ab4d4e6125567bd979994f13bb1f23dddd", "1")

	// 2
	setUpMocks(apiAddr, "842783e3005495d5d1637f5364b59343c7844707", "2")

	client := &http.Client{Transport: &http.Transport{}}
	gock.InterceptClient(client)

	qBTConn.Init(apiAddr, client, false)
	server := httptest.NewServer(http.HandlerFunc(handler))
	defer server.Close()
	defer server.CloseClientConnections()
	serverAddr := server.Listener.Addr().(*net.TCPAddr)

	transmissionbt, err := transmissionrpc.New(serverAddr.IP.String(), testBridgeUser, testBridgePass,
		&transmissionrpc.AdvancedConfig{Port: uint16(serverAddr.Port)})
	Check(err)
	torrents, err := transmissionbt.TorrentGetAll()
	Check(err)
	if len(torrents) != 2 {
		t.Error("Number of torrents is not equal to 2")
	}
	if *torrents[0].Name != "ubuntu-18.04.2-desktop-amd64.iso" {
		t.Error("Unexpected torrent 0")
	}
	if *torrents[1].Name != "ubuntu-18.04.2-live-server-amd64.iso" {
		t.Error("Unexpected torrent 1")
	}

	id := *torrents[1].ID

	singleTorrent, err := transmissionbt.TorrentGetAllFor([]int64{id})
	Check(err)
	if *singleTorrent[0].Name != "ubuntu-18.04.2-live-server-amd64.iso" {
		t.Error("Unexpected torrent")
	}
}

func TestTorrentListingRepeated(t *testing.T) {
	prevLogLevel := currentLogLevel
	currentLogLevel = log.ErrorLevel
	for i := 0; i < 50; i++ {
		TestTorrentListing(t)
	}
	currentLogLevel = prevLogLevel
}

func TestSyncing(t *testing.T) {
	const apiAddr = "http://localhost:8080"
	log.SetLevel(currentLogLevel)

	defer gock.Off()

	gock.Observe(gock.DumpRequest)

	gock.New(apiAddr).
		Post("/api/v2/auth/login").
		Reply(200).
		SetHeader("Set-Cookie", "SID=1")
	setUpVersionMocks(apiAddr, "2.2", "v4.1.5")

	setUpSyncEndpoint(apiAddr)

	// 1
	setUpMocks(apiAddr, "cf7da7ab4d4e6125567bd979994f13bb1f23dddd", "1")

	// 2
	setUpMocks(apiAddr, "842783e3005495d5d1637f5364b59343c7844707", "2")

	// 3
	setUpMocks(apiAddr, "7a1448be6d15bcde08ee9915350d0725775b73a3", "3")

	client := &http.Client{Transport: &http.Transport{}}
	gock.InterceptClient(client)

	qBTConn.Init(apiAddr, client, true)
	server := httptest.NewServer(http.HandlerFunc(handler))
	defer server.Close()
	defer server.CloseClientConnections()
	serverAddr := server.Listener.Addr().(*net.TCPAddr)

	transmissionbt, err := transmissionrpc.New(serverAddr.IP.String(), testBridgeUser, testBridgePass,
		&transmissionrpc.AdvancedConfig{Port: uint16(serverAddr.Port)})
	Check(err)
	torrents, err := transmissionbt.TorrentGetAll()
	Check(err)
	if len(torrents) != 2 {
		t.Error("Number of torrents is not equal to 2")
	}
	if *torrents[0].Name != "ubuntu-18.04.2-live-server-amd64.iso" {
		t.Error("Unexpected torrent 0")
	}
	if *torrents[1].Name != "ubuntu-18.04.2-desktop-amd64.iso" {
		t.Error("Unexpected torrent 1")
	}

	torrents, err = transmissionbt.TorrentGetAll()
	Check(err)
	if len(torrents) != 2 {
		t.Error("Number of torrents is not equal to 2")
	}

	torrents, err = transmissionbt.TorrentGetAll()
	Check(err)
	if len(torrents) != 3 {
		t.Error("Number of torrents is not equal to 3")
	}
	if *torrents[2].Name != "xubuntu-18.04.2-desktop-amd64.iso" {
		t.Error("Unexpected torrent name")
	}

	torrents, err = transmissionbt.TorrentGetAll()
	Check(err)
	if len(torrents) != 3 {
		t.Error("Number of torrents is not equal to 3")
	}

	torrents, err = transmissionbt.TorrentGetAll()
	Check(err)
	if len(torrents) != 2 {
		t.Error("Number of torrents is not equal to 2")
	}

	id := *torrents[1].ID

	singleTorrent, err := transmissionbt.TorrentGetAllFor([]int64{id})
	Check(err)
	if *singleTorrent[0].Name != "ubuntu-18.04.2-desktop-amd64.iso" {
		t.Error("Unexpected torrent")
	}
}

func TestSyncingRecentlyActive(t *testing.T) {
	const apiAddr = "http://localhost:8080"
	log.SetLevel(currentLogLevel)

	defer gock.Off()

	gock.Observe(gock.DumpRequest)

	gock.New(apiAddr).
		Post("/api/v2/auth/login").
		Reply(200).
		SetHeader("Set-Cookie", "SID=1")
	setUpVersionMocks(apiAddr, "2.2", "v4.1.5")

	setUpSyncEndpoint(apiAddr)

	// 1
	setUpMocks(apiAddr, "cf7da7ab4d4e6125567bd979994f13bb1f23dddd", "1")

	// 2
	setUpMocks(apiAddr, "842783e3005495d5d1637f5364b59343c7844707", "2")

	// 3
	setUpMocks(apiAddr, "7a1448be6d15bcde08ee9915350d0725775b73a3", "3")

	client := &http.Client{Transport: &http.Transport{}}
	gock.InterceptClient(client)

	qBTConn.Init(apiAddr, client, true)
	server := httptest.NewServer(http.HandlerFunc(handler))
	defer server.Close()
	defer server.CloseClientConnections()
	serverAddr := server.Listener.Addr().(*net.TCPAddr)

	transmissionbt, err := transmissionrpc.New(serverAddr.IP.String(), testBridgeUser, testBridgePass,
		&transmissionrpc.AdvancedConfig{Port: uint16(serverAddr.Port)})
	Check(err)

	gock.New(apiAddr).
		Post("/api/v2/torrents/resume").
		MatchType("url").
		BodyString("^hashes=842783e3005495d5d1637f5364b59343c7844707%7Ccf7da7ab4d4e6125567bd979994f13bb1f23dddd$").
		Times(2).
		Reply(200)
	gock.New(apiAddr).
		Post("/api/v2/torrents/resume").
		MatchType("url").
		BodyString("^hashes=842783e3005495d5d1637f5364b59343c7844707%7Ccf7da7ab4d4e6125567bd979994f13bb1f23dddd%7C7a1448be6d15bcde08ee9915350d0725775b73a3$").
		Times(5).
		Reply(200)
	gock.New(apiAddr).
		Post("/api/v2/torrents/resume").
		MatchType("url").
		BodyString("^hashes=842783e3005495d5d1637f5364b59343c7844707%7Ccf7da7ab4d4e6125567bd979994f13bb1f23dddd$").
		Times(1).
		Reply(200)

	for i := 0; i < 5; i++ {
		err = transmissionbt.TorrentStartRecentlyActive()
		Check(err)
	}
}

func TestSyncingRecentlyActiveLong(t *testing.T) {
	const apiAddr = "http://localhost:8080"
	log.SetLevel(currentLogLevel)

	defer gock.Off()

	prevTime := qBT.RECENTLY_ACTIVE_TIMEOUT
	qBT.RECENTLY_ACTIVE_TIMEOUT = 5 * time.Second

	gock.Observe(gock.DumpRequest)

	gock.New(apiAddr).
		Post("/api/v2/auth/login").
		Reply(200).
		SetHeader("Set-Cookie", "SID=1")
	setUpVersionMocks(apiAddr, "2.2", "v4.1.5")

	setUpSyncEndpoint(apiAddr)

	// 1
	setUpMocks(apiAddr, "cf7da7ab4d4e6125567bd979994f13bb1f23dddd", "1")

	// 2
	setUpMocks(apiAddr, "842783e3005495d5d1637f5364b59343c7844707", "2")

	// 3
	setUpMocks(apiAddr, "7a1448be6d15bcde08ee9915350d0725775b73a3", "3")

	client := &http.Client{Transport: &http.Transport{}}
	gock.InterceptClient(client)

	qBTConn.Init(apiAddr, client, true)
	server := httptest.NewServer(http.HandlerFunc(handler))
	defer server.Close()
	defer server.CloseClientConnections()
	serverAddr := server.Listener.Addr().(*net.TCPAddr)

	transmissionbt, err := transmissionrpc.New(serverAddr.IP.String(), testBridgeUser, testBridgePass,
		&transmissionrpc.AdvancedConfig{Port: uint16(serverAddr.Port)})
	Check(err)

	torrents, err := transmissionbt.TorrentGetAll()
	Check(err)
	if len(torrents) != 2 {
		t.Error("Number of torrents is not equal to 2")
	}

	time.Sleep(qBT.RECENTLY_ACTIVE_TIMEOUT + 5*time.Second)

	// Nothing recently active: no qBT call at all (there is no mock for one).
	err = transmissionbt.TorrentStartRecentlyActive()
	Check(err)

	gock.New(apiAddr).
		Post("/api/v2/torrents/resume").
		MatchType("url").
		BodyString("^hashes=7a1448be6d15bcde08ee9915350d0725775b73a3$").
		Reply(200)
	err = transmissionbt.TorrentStartRecentlyActive()
	Check(err)

	qBT.RECENTLY_ACTIVE_TIMEOUT = prevTime
}

func TestTorrentAdd(t *testing.T) {
	const apiAddr = "http://localhost:8080"
	log.SetLevel(currentLogLevel)

	defer gock.Off()

	gock.Observe(gock.DumpRequest)

	gock.New(apiAddr).
		Post("/api/v2/auth/login").
		Reply(200).
		SetHeader("Set-Cookie", "SID=1")
	setUpVersionMocks(apiAddr, "2.2", "v4.1.5")

	setUpSyncEndpoint(apiAddr)

	// 1
	setUpMocks(apiAddr, "cf7da7ab4d4e6125567bd979994f13bb1f23dddd", "1")

	// 2
	setUpMocks(apiAddr, "842783e3005495d5d1637f5364b59343c7844707", "2")

	// 3
	setUpMocks(apiAddr, "7a1448be6d15bcde08ee9915350d0725775b73a3", "3")

	client := &http.Client{Transport: &http.Transport{}}
	gock.InterceptClient(client)

	qBTConn.Init(apiAddr, client, true)
	server := httptest.NewServer(http.HandlerFunc(handler))
	defer server.Close()
	defer server.CloseClientConnections()
	serverAddr := server.Listener.Addr().(*net.TCPAddr)

	transmissionbt, err := transmissionrpc.New(serverAddr.IP.String(), testBridgeUser, testBridgePass,
		&transmissionrpc.AdvancedConfig{Port: uint16(serverAddr.Port)})
	Check(err)

	gock.New(apiAddr).
		Post("/api/v2/torrents/add").
		MatchType("form").
		BodyString("Content-Disposition: form-data; name=\"urls\"").
		Reply(200)

	magnet := "magnet:?xt=urn:btih:7a1448be6d15bcde08ee9915350d0725775b73a3&dn=xubuntu-18.04.2-desktop-amd64.iso&tr=http%3a%2f%2ftorrent.ubuntu.com%3a6969%2fannounce"

	torrent, err := transmissionbt.TorrentAdd(&transmissionrpc.TorrentAddPayload{Filename: &magnet})
	Check(err)

	if *torrent.Name != "xubuntu-18.04.2-desktop-amd64.iso" {
		t.Error("Unexpected torrent")
	}
	addedID := *torrent.ID

	// The same magnet again is a duplicate, answered without calling torrents/add
	// (no mock left for it). A trailing "+sf" is just part of the path now.
	location := "/home/user/+sf"
	torrent, err = transmissionbt.TorrentAdd(&transmissionrpc.TorrentAddPayload{Filename: &magnet, DownloadDir: &location})
	Check(err)
	if *torrent.ID != addedID || *torrent.HashString != "7a1448be6d15bcde08ee9915350d0725775b73a3" {
		t.Errorf("duplicate: id %d hash %s, want id %d", *torrent.ID, *torrent.HashString, addedID)
	}
}

func TestTorrentMove(t *testing.T) {
	const apiAddr = "http://localhost:8080"
	log.SetLevel(currentLogLevel)

	defer gock.Off()
	gock.Flush()
	gock.CleanUnmatchedRequest()

	gock.Observe(gock.DumpRequest)

	gock.New(apiAddr).
		Post("/api/v2/auth/login").
		Reply(200).
		SetHeader("Set-Cookie", "SID=1")
	setUpVersionMocks(apiAddr, "2.2", "v4.1.5")

	gock.New(apiAddr).
		Get("/api/v2/torrents/info").
		Reply(200).
		File("testdata/torrent_list.json")

	client := &http.Client{Transport: &http.Transport{}}
	gock.InterceptClient(client)

	qBTConn.Init(apiAddr, client, false)
	server := httptest.NewServer(http.HandlerFunc(handler))
	defer server.Close()
	defer server.CloseClientConnections()
	serverAddr := server.Listener.Addr().(*net.TCPAddr)

	transmissionbt, err := transmissionrpc.New(serverAddr.IP.String(), testBridgeUser, testBridgePass,
		&transmissionrpc.AdvancedConfig{Port: uint16(serverAddr.Port)})
	Check(err)

	// The old +s/+f/+h path flags are gone: the location is passed through as is.
	gock.New(apiAddr).
		Post("/api/v2/torrents/setLocation").
		MatchType("url").
		BodyString("^hashes=842783e3005495d5d1637f5364b59343c7844707&location=%2Fnew%2Fdir%2Bsf$").
		Reply(200)

	err = transmissionbt.TorrentSetLocationHash("842783e3005495d5d1637f5364b59343c7844707", "/new/dir+sf", true)
	Check(err)

	p := gock.Pending()
	println(p)

	if gock.IsPending() {
		t.Fail()
	}
}

// setUpVersionMocks answers the version probe that follows the (single) login.
// The 2019 testdata is from the qBT 4.1 era (WebAPI 2.x < 2.11: pause/resume).
func setUpVersionMocks(apiAddr, webapiVersion, appVersion string) {
	gock.New(apiAddr).
		Get("/api/v2/app/webapiVersion").
		Reply(200).
		BodyString(webapiVersion)
	gock.New(apiAddr).
		Get("/api/v2/app/version").
		Reply(200).
		BodyString(appVersion)
}

func setUpSyncEndpoint(apiAddr string) {
	gock.New(apiAddr).
		Get("/api/v2/sync/maindata").
		MatchParam("rid", "0").
		HeaderPresent("Cookie").
		Reply(200).
		File("testdata/sync_initial.json")
	gock.New(apiAddr).
		Get("/api/v2/sync/maindata").
		MatchParam("rid", "1").
		HeaderPresent("Cookie").
		Reply(200).
		File("testdata/sync_1.json")
	gock.New(apiAddr).
		Get("/api/v2/sync/maindata").
		MatchParam("rid", "2").
		HeaderPresent("Cookie").
		Reply(200).
		File("testdata/sync_2.json")
	gock.New(apiAddr).
		Get("/api/v2/sync/maindata").
		MatchParam("rid", "3").
		HeaderPresent("Cookie").
		Reply(200).
		File("testdata/sync_3.json")
	gock.New(apiAddr).
		Get("/api/v2/sync/maindata").
		MatchParam("rid", "4").
		HeaderPresent("Cookie").
		Reply(200).
		File("testdata/sync_4.json")
	gock.New(apiAddr).
		Get("/api/v2/sync/maindata").
		MatchParam("rid", "5").
		HeaderPresent("Cookie").
		Reply(200).
		File("testdata/sync_5.json")
}

func TestSyncingRepeated(t *testing.T) {
	prevLogLevel := currentLogLevel
	currentLogLevel = log.ErrorLevel
	for i := 0; i < 50; i++ {
		TestSyncing(t)
	}
	currentLogLevel = prevLogLevel
}

func setUpMocks(apiAddr string, hash string, name string) {
	gock.New(apiAddr).
		Get("/api/v2/torrents/properties").
		MatchParam("hash", hash).
		Persist().
		Reply(200).
		File("testdata/torrent_" + name + "_properties.json")
	gock.New(apiAddr).
		Get("/api/v2/torrents/trackers").
		MatchParam("hash", hash).
		Persist().
		Reply(200).
		File("testdata/torrent_" + name + "_trackers.json")
	gock.New(apiAddr).
		Get("/api/v2/torrents/pieceStates").
		MatchParam("hash", hash).
		Persist().
		Reply(200).
		File("testdata/torrent_" + name + "_piecestates.json")
	gock.New(apiAddr).
		Get("/api/v2/torrents/files").
		MatchParam("hash", hash).
		Persist().
		Reply(200).
		File("testdata/torrent_" + name + "_files.json")
	gock.New(apiAddr).
		Get("/api/v2/sync/torrentPeers").
		MatchParam("hash", hash).
		MatchParam("rid", "0").
		Persist().
		Reply(200).
		File("testdata/torrent_" + name + "_peers.json")
}
