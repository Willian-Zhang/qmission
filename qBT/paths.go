package qBT

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	log "github.com/sirupsen/logrus"
)

// RenamePathAPIVersion is the first WebAPI with torrents/renameFolder and the
// oldPath/newPath form of torrents/renameFile (qBittorrent 4.3.3; the API docs
// list it as 2.8.0, but 4.3.3 shipped it as 2.7.0).
var RenamePathAPIVersion = APIVersion{2, 7, 0}

var (
	// ErrQueueingDisabled: topPrio/bottomPrio/increasePrio/decreasePrio answered
	// 409 "Torrent queueing must be enabled".
	ErrQueueingDisabled = errors.New("torrent queueing is disabled in qBittorrent")
	// ErrRenameUnsupported: renameFile/renameFolder need WebAPI >= 2.7 (qBittorrent 4.3.3).
	ErrRenameUnsupported = errors.New("renaming files and folders needs qBittorrent 4.3.3 or newer (WebAPI 2.7)")
	// ErrRenameRejected: renameFile/renameFolder answered 409 (no such path, the
	// target exists, or the name has invalid characters).
	ErrRenameRejected = errors.New("qBittorrent refused the rename (the target already exists, or the name is invalid)")
	// ErrInvalidName: torrents/rename answered 409 "Incorrect torrent name" (empty name).
	ErrInvalidName = errors.New("qBittorrent refused the torrent name")
	// ErrLocationEmpty: setLocation answered 400 "Save path cannot be empty".
	ErrLocationEmpty = errors.New("qBittorrent refused an empty location")
	// ErrCannotCreateLocation: setLocation answered 409 "Cannot make save path".
	ErrCannotCreateLocation = errors.New("qBittorrent couldn't create the new location")
)

// statusOf returns the HTTP status of a qBT HTTPError, or 0.
func statusOf(err error) int {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode
	}
	return 0
}

// mapStatus replaces qBT HTTP errors listed in byStatus with the sentinel. The
// qBT message is logged at debug only: rename and location errors quote paths.
func mapStatus(err error, path string, byStatus map[int]error) error {
	if err == nil {
		return nil
	}
	if sentinel, ok := byStatus[statusOf(err)]; ok {
		log.WithError(err).WithField("path", path).Debug("qBittorrent rejected the request")
		return fmt.Errorf("%w (HTTP %d)", sentinel, statusOf(err))
	}
	return err
}

// SetLocation calls torrents/setLocation. qBT always moves the data, creates
// the directory if needed, and turns automatic torrent management off.
func (q *Connection) SetLocation(hashes []Hash, location string) error {
	_, err := q.PostForm("torrents/setLocation", url.Values{
		"hashes":   {joinHashes(hashes)},
		"location": {location},
	})
	return mapStatus(err, "torrents/setLocation", map[int]error{
		http.StatusBadRequest: ErrLocationEmpty,
		http.StatusConflict:   ErrCannotCreateLocation,
	})
}

// RenameTorrent calls torrents/rename, which changes only the display name.
func (q *Connection) RenameTorrent(hash Hash, name string) error {
	_, err := q.PostForm("torrents/rename", url.Values{"hash": {string(hash)}, "name": {name}})
	return mapStatus(err, "torrents/rename", map[int]error{
		http.StatusNotFound: ErrTorrentNotFound,
		http.StatusConflict: ErrInvalidName,
	})
}

// SupportsRenamePath reports whether renameFile/renameFolder take oldPath/newPath.
func (q *Connection) SupportsRenamePath() (bool, error) {
	if _, err := q.ensureLoggedIn(); err != nil {
		return false, err
	}
	return q.APIVersion().AtLeast(RenamePathAPIVersion), nil
}

func (q *Connection) renamePath(endpoint string, hash Hash, oldPath, newPath string) error {
	ok, err := q.SupportsRenamePath()
	if err != nil {
		return err
	}
	if !ok {
		return ErrRenameUnsupported
	}
	_, err = q.PostForm(endpoint, url.Values{
		"hash":    {string(hash)},
		"oldPath": {oldPath},
		"newPath": {newPath},
	})
	return mapStatus(err, endpoint, map[int]error{
		http.StatusNotFound: ErrTorrentNotFound,
		http.StatusConflict: ErrRenameRejected,
	})
}

// RenameFile calls torrents/renameFile. Paths are relative to the save path,
// "/"-separated, as in torrents/files; newPath is the whole new path.
func (q *Connection) RenameFile(hash Hash, oldPath, newPath string) error {
	return q.renamePath("torrents/renameFile", hash, oldPath, newPath)
}

// RenameFolder calls torrents/renameFolder (WebAPI >= 2.7).
func (q *Connection) RenameFolder(hash Hash, oldPath, newPath string) error {
	return q.renamePath("torrents/renameFolder", hash, oldPath, newPath)
}

// SetForceStart calls torrents/setForceStart. value=true also starts a stopped
// torrent (4.6.4 resume(Forced), 5.3.0 start(Forced)).
func (q *Connection) SetForceStart(hashes []Hash, value bool) error {
	_, err := q.PostForm("torrents/setForceStart", url.Values{
		"hashes": {joinHashes(hashes)},
		"value":  {strconv.FormatBool(value)},
	})
	return err
}

// QueueMove is a qBT queue endpoint: torrents/topPrio, bottomPrio,
// increasePrio (one up) or decreasePrio (one down).
type QueueMove string

const (
	QueueTop    QueueMove = "torrents/topPrio"
	QueueBottom QueueMove = "torrents/bottomPrio"
	QueueUp     QueueMove = "torrents/increasePrio"
	QueueDown   QueueMove = "torrents/decreasePrio"
)

// MoveInQueue calls a queue endpoint. qBT skips torrents outside the queue
// (seeding, or no queue position) and answers 409 when queueing is off.
func (q *Connection) MoveInQueue(move QueueMove, hashes []Hash) error {
	err := q.PostWithHashes(string(move), hashes)
	return mapStatus(err, string(move), map[int]error{http.StatusConflict: ErrQueueingDisabled})
}
