package qBT

// Per-torrent setters used by Transmission's torrent-set. Parameter
// requirements were checked in the qBittorrent sources (see
// scrachpad/findings/qbittorrent-source.md, "torrent-set endpoints").

import (
	"net/url"
	"strconv"
	"strings"
)

var (
	// InactiveSeedingAPIVersion is WebAPI 2.9.2 = qBittorrent 4.6.0: torrents/setShareLimits
	// requires inactiveSeedingTimeLimit (4.5.5 = 2.8.19 has no such limit).
	InactiveSeedingAPIVersion = APIVersion{2, 9, 2}
	// ShareLimitActionAPIVersion: torrents/setShareLimits requires shareLimitAction (WebAPI changelog 2.12.0).
	ShareLimitActionAPIVersion = APIVersion{2, 12, 0}
	// ShareLimitsModeAPIVersion: torrents/setShareLimits requires shareLimitsMode (WebAPI changelog 2.15.3).
	ShareLimitsModeAPIVersion = APIVersion{2, 15, 3}
)

// ShareLimits are the torrents/setShareLimits values. Limits use qBT's
// convention: -2 = use the global limit, -1 = unlimited; times in minutes.
// Action and Mode are only sent where the WebAPI requires them; "" sends "Default".
type ShareLimits struct {
	RatioLimit               float64
	SeedingTimeLimit         int64
	InactiveSeedingTimeLimit int64
	Action                   string
	Mode                     string
}

// SetTorrentsShareLimits calls torrents/setShareLimits, sending exactly the
// parameters this WebAPI version requires. inactiveSeedingTimeLimit is left
// out before WebAPI 2.9.2, where qBT has no such limit.
func (q *Connection) SetTorrentsShareLimits(hashes []Hash, l ShareLimits) error {
	if _, err := q.ensureLoggedIn(); err != nil {
		return err
	}
	v := q.APIVersion()
	form := url.Values{
		"hashes":           {joinHashes(hashes)},
		"ratioLimit":       {strconv.FormatFloat(l.RatioLimit, 'f', -1, 64)},
		"seedingTimeLimit": {strconv.FormatInt(l.SeedingTimeLimit, 10)},
	}
	if v.AtLeast(InactiveSeedingAPIVersion) {
		form.Set("inactiveSeedingTimeLimit", strconv.FormatInt(l.InactiveSeedingTimeLimit, 10))
	}
	orDefault := func(s string) string {
		if s == "" {
			return "Default"
		}
		return s
	}
	if v.AtLeast(ShareLimitActionAPIVersion) {
		form.Set("shareLimitAction", orDefault(l.Action))
	}
	if v.AtLeast(ShareLimitsModeAPIVersion) {
		form.Set("shareLimitsMode", orDefault(l.Mode))
	}
	_, err := q.PostForm("torrents/setShareLimits", form)
	return err
}

// SetTorrentsDownloadLimit calls torrents/setDownloadLimit (B/s; qBT stores 0 and -1 as unlimited).
func (q *Connection) SetTorrentsDownloadLimit(hashes []Hash, limit int64) error {
	_, err := q.PostForm("torrents/setDownloadLimit", url.Values{
		"hashes": {joinHashes(hashes)},
		"limit":  {strconv.FormatInt(limit, 10)},
	})
	return err
}

// SetTorrentsUploadLimit calls torrents/setUploadLimit (B/s; qBT stores 0 and -1 as unlimited).
func (q *Connection) SetTorrentsUploadLimit(hashes []Hash, limit int64) error {
	_, err := q.PostForm("torrents/setUploadLimit", url.Values{
		"hashes": {joinHashes(hashes)},
		"limit":  {strconv.FormatInt(limit, 10)},
	})
	return err
}

// AddTorrentTags calls torrents/addTags; qBT creates tags it doesn't know yet.
func (q *Connection) AddTorrentTags(hashes []Hash, tags []string) error {
	_, err := q.PostForm("torrents/addTags", url.Values{
		"hashes": {joinHashes(hashes)},
		"tags":   {strings.Join(tags, ",")},
	})
	return err
}

// AddTorrentTrackers calls torrents/addTrackers for one torrent. urls is one
// URL per line; each blank line moves to the next tier (parseTrackerEntries),
// and URLs the torrent already has are skipped by qBT.
func (q *Connection) AddTorrentTrackers(hash Hash, urls string) error {
	_, err := q.PostForm("torrents/addTrackers", url.Values{
		"hash": {string(hash)},
		"urls": {urls},
	})
	return err
}

// RemoveTorrentTrackers calls torrents/removeTrackers for one torrent (URLs "|"-joined).
func (q *Connection) RemoveTorrentTrackers(hash Hash, urls []string) error {
	_, err := q.PostForm("torrents/removeTrackers", url.Values{
		"hash": {string(hash)},
		"urls": {strings.Join(urls, "|")},
	})
	return err
}

// EditTorrentTracker calls torrents/editTracker. The original URL is sent as
// both `origUrl` (qBT 4.x to 5.1) and `url` (5.3); each version ignores the other.
func (q *Connection) EditTorrentTracker(hash Hash, origURL, newURL string) error {
	_, err := q.PostForm("torrents/editTracker", url.Values{
		"hash":    {string(hash)},
		"origUrl": {origURL},
		"url":     {origURL},
		"newUrl":  {newURL},
	})
	return err
}

// ToggleSequential calls torrents/toggleSequentialDownload (a toggle: compare first).
func (q *Connection) ToggleSequential(hashes []Hash) error {
	return q.PostWithHashes("torrents/toggleSequentialDownload", hashes)
}

// ToggleFirstLastPiece calls torrents/toggleFirstLastPiecePrio (a toggle: compare first).
func (q *Connection) ToggleFirstLastPiece(hashes []Hash) error {
	return q.PostWithHashes("torrents/toggleFirstLastPiecePrio", hashes)
}
