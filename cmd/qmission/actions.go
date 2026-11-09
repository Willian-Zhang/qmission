package main

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Willian-Zhang/qmission/qBT"
	log "github.com/sirupsen/logrus"
)

// torrentAction runs one qBT call for the torrents selected by `ids` (rpc-spec
// 3.1: absent = all, unknown IDs skipped). With nothing selected qBT isn't
// called at all.
func torrentAction(method string, args json.RawMessage, do func([]qBT.Hash) error) (JsonMap, error) {
	torrents, err := parseActionArgument(args)
	if err != nil {
		return nil, err
	}
	log.WithField("method", method).WithField("count", len(torrents)).Debug("Torrent action")
	if len(torrents) == 0 {
		return JsonMap{}, nil
	}
	if err := do(torrents.Hashes()); err != nil {
		return nil, err
	}
	return JsonMap{}, nil
}

// TorrentStop uses torrents/stop (qBT 5.x) or torrents/pause (4.x).
func TorrentStop(args json.RawMessage) (JsonMap, error) {
	return torrentAction("torrent-stop", args, qBTConn.StopTorrents)
}

// TorrentStart uses torrents/start (qBT 5.x) or torrents/resume (4.x).
func TorrentStart(args json.RawMessage) (JsonMap, error) {
	return torrentAction("torrent-start", args, qBTConn.StartTorrents)
}

// TorrentRecheck is torrent-verify: torrents/recheck.
func TorrentRecheck(args json.RawMessage) (JsonMap, error) {
	return torrentAction("torrent-verify", args, qBTConn.RecheckTorrents)
}

// TorrentReannounce is torrent-reannounce: torrents/reannounce.
func TorrentReannounce(args json.RawMessage) (JsonMap, error) {
	return torrentAction("torrent-reannounce", args, qBTConn.ReannounceTorrents)
}

// TorrentDelete is torrent-remove: torrents/delete, `delete-local-data` → deleteFiles.
func TorrentDelete(args json.RawMessage) (JsonMap, error) {
	var req struct {
		DeleteLocalData interface{} `json:"delete-local-data"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	deleteFiles := parseDeleteFilesField(req.DeleteLocalData)
	return torrentAction("torrent-remove", args, func(hashes []qBT.Hash) error {
		log.WithField("count", len(hashes)).WithField("deleteFiles", deleteFiles).Info("Removing torrents")
		return qBTConn.DeleteTorrents(hashes, deleteFiles)
	})
}

func parseDeleteFilesField(deleteLocalData interface{}) bool {
	switch val := deleteLocalData.(type) {
	case bool:
		return val
	case float64:
		return val != 0
	default:
		return false // Default value
	}
}

// FreeSpace answers free-space from qBT's server_state.free_space_on_disk,
// which is the free space of qBT's default save path whatever `path` asks for
// (the bridge's own disk means nothing in Docker). qBT reports no capacity, so
// total_size is -1, Transmission's value for "unknown". Without -sync there is
// no server_state and size-bytes is -1 too.
func FreeSpace(args json.RawMessage) (JsonMap, error) {
	var req struct {
		Path *string `json:"path"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, fmt.Errorf("free-space: invalid arguments: %w", err)
	}
	if req.Path == nil {
		return nil, errors.New("free-space: directory path argument is missing")
	}
	size := int64(-1)
	if *useSync {
		state, err := serverState()
		if err != nil {
			return nil, err
		}
		size = state.Free_space_on_disk
	}
	return JsonMap{"path": *req.Path, "size-bytes": size, "total_size": int64(-1)}, nil
}
