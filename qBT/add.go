package qBT

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// AddParams is one torrents/add call: URLs (magnet links or http(s) URLs to a
// .torrent) and/or one .torrent file. Zero values leave qBT's defaults.
type AddParams struct {
	URLs               []string
	Torrent            []byte // .torrent content, nil for none
	Cookie             string // for URL downloads; qBT 5.x no longer reads it
	SavePath           string
	Tags               []string // created by qBT if missing (4.6 and 5.x)
	Stopped            *bool    // nil = qBT's "add stopped" preference
	Sequential         bool     // sequentialDownload
	FirstLastPiecePrio bool     // firstLastPiecePrio
}

// AddResult is what torrents/add reports. Only WebAPI >= 2.14 lists the IDs;
// older versions answer "Ok." and the result is empty.
type AddResult struct {
	AddedIDs []Hash
	Pending  int // URL downloads qBT hasn't finished yet (WebAPI >= 2.14, HTTP 202)
}

// AddTorrent calls torrents/add (multipart). It returns ErrAddRejected when qBT
// added nothing: "Fails." before WebAPI 2.14, HTTP 409 from 2.14 on.
func (q *Connection) AddTorrent(p AddParams) (AddResult, error) {
	if _, err := q.ensureLoggedIn(); err != nil { // the stopped/paused name needs the version
		return AddResult{}, err
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if p.Torrent != nil {
		w, err := mw.CreateFormFile("torrents", "upload.torrent")
		if err != nil {
			return AddResult{}, err
		}
		if _, err := w.Write(p.Torrent); err != nil {
			return AddResult{}, err
		}
	}
	var fields [][2]string
	if len(p.URLs) > 0 {
		fields = append(fields, [2]string{"urls", strings.Join(p.URLs, "\n")})
	}
	if p.Cookie != "" {
		fields = append(fields, [2]string{"cookie", p.Cookie})
	}
	if p.SavePath != "" {
		fields = append(fields, [2]string{"savepath", p.SavePath})
	}
	if len(p.Tags) > 0 {
		fields = append(fields, [2]string{"tags", strings.Join(p.Tags, ",")})
	}
	if p.Stopped != nil {
		fields = append(fields, [2]string{q.Gates().AddStoppedParam, strconv.FormatBool(*p.Stopped)})
	}
	if p.Sequential {
		fields = append(fields, [2]string{"sequentialDownload", "true"})
	}
	if p.FirstLastPiecePrio {
		fields = append(fields, [2]string{"firstLastPiecePrio", "true"})
	}
	for _, f := range fields {
		if err := mw.WriteField(f[0], f[1]); err != nil {
			return AddResult{}, err
		}
	}
	if err := mw.Close(); err != nil {
		return AddResult{}, err
	}

	data, err := q.do("POST", "torrents/add", nil, mw.FormDataContentType(), buf.Bytes())
	if err != nil {
		var httpErr *HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusConflict {
			return AddResult{}, ErrAddRejected
		}
		return AddResult{}, err
	}
	return parseAddResponse(data)
}

func parseAddResponse(data []byte) (AddResult, error) {
	body := bytes.TrimSpace(data)
	switch string(body) {
	case "Fails.":
		return AddResult{}, ErrAddRejected
	case "", "Ok.":
		return AddResult{}, nil
	}
	var resp struct {
		Success_count     int
		Failure_count     int
		Pending_count     int
		Added_torrent_ids []Hash
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return AddResult{}, decodeError("torrents/add", data, err)
	}
	if resp.Success_count == 0 && resp.Pending_count == 0 {
		return AddResult{}, ErrAddRejected
	}
	return AddResult{AddedIDs: resp.Added_torrent_ids, Pending: resp.Pending_count}, nil
}

// SetFilePriority sets one priority (0, 1, 6 or 7) on several files of a torrent
// with a single torrents/filePrio call (`id` is "|"-separated in 4.6 and 5.x).
func (q *Connection) SetFilePriority(hash Hash, fileIDs []int, priority int) error {
	ids := make([]string, len(fileIDs))
	for i, id := range fileIDs {
		ids[i] = strconv.Itoa(id)
	}
	_, err := q.PostForm("torrents/filePrio", url.Values{
		"hash":     {string(hash)},
		"id":       {strings.Join(ids, "|")},
		"priority": {strconv.Itoa(priority)},
	})
	return err
}

// GetTags lists every tag qBT knows (torrents/tags, a JSON array).
func (q *Connection) GetTags() (tags []string, err error) {
	err = q.getJSON("torrents/tags", nil, &tags)
	return
}

// RemoveTags takes tags off torrents (torrents/removeTags); the tags stay defined.
func (q *Connection) RemoveTags(hashes []Hash, tags []string) error {
	_, err := q.PostForm("torrents/removeTags", url.Values{
		"hashes": {joinHashes(hashes)},
		"tags":   {strings.Join(tags, ",")},
	})
	return err
}

// DeleteTags deletes tags globally (torrents/deleteTags), also from every torrent.
func (q *Connection) DeleteTags(tags []string) error {
	_, err := q.PostForm("torrents/deleteTags", url.Values{"tags": {strings.Join(tags, ",")}})
	return err
}

// ReannounceTorrents calls torrents/reannounce (trackers; 5.x also DHT).
func (q *Connection) ReannounceTorrents(hashes []Hash) error {
	return q.PostWithHashes("torrents/reannounce", hashes)
}

// FindByInfoHash finds a torrent by its v1 (40 hex) and/or v2 (64 hex) info
// hash; either may be "". qBT keys torrents by the v1 hash, or by the v2 hash
// truncated to 40 hex for v2-only torrents (and hybrid ones with libtorrent 2),
// so this also compares the infohash_v1/infohash_v2 fields.
func (list *TorrentsList) FindByInfoHash(v1, v2 string) *TorrentInfo {
	v1, v2 = strings.ToLower(v1), strings.ToLower(v2)
	list.mutex.RLock()
	defer list.mutex.RUnlock()
	if v1 != "" {
		if t := list.items[Hash(v1)]; t != nil {
			return t
		}
	}
	if len(v2) == 64 {
		if t := list.items[Hash(v2[:40])]; t != nil {
			return t
		}
	}
	for _, t := range list.items {
		if (v1 != "" && strings.EqualFold(t.Infohash_v1, v1)) || (v2 != "" && strings.EqualFold(t.Infohash_v2, v2)) {
			return t
		}
	}
	return nil
}
