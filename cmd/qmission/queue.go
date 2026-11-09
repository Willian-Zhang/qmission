package main

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Willian-Zhang/qmission/qBT"
)

func init() {
	registerRPC("queue-move-top", queueMoveHandler("queue-move-top", qBT.QueueTop))
	registerRPC("queue-move-bottom", queueMoveHandler("queue-move-bottom", qBT.QueueBottom))
	registerRPC("queue-move-up", queueMoveHandler("queue-move-up", qBT.QueueUp))
	registerRPC("queue-move-down", queueMoveHandler("queue-move-down", qBT.QueueDown))
	registerRPC("torrent-start-now", TorrentStartNow)
}

// queueMoveHandler maps queue-move-* to topPrio/bottomPrio/increasePrio/
// decreasePrio. "Up" is towards the front of the queue: qBT `priority` 1 and
// Transmission queuePosition 0 (see queuePositions). qBT moves only torrents
// that are in its queue and keeps their relative order.
func queueMoveHandler(method string, move qBT.QueueMove) rpcHandler {
	return func(args json.RawMessage) (JsonMap, error) {
		resp, err := torrentAction(method, args, func(hashes []qBT.Hash) error {
			return qBTConn.MoveInQueue(move, hashes)
		})
		if errors.Is(err, qBT.ErrQueueingDisabled) {
			return nil, fmt.Errorf("%s: %w; enable queueing in qBittorrent (Options > BitTorrent > Torrent Queueing)", method, err)
		}
		return resp, err
	}
}

// TorrentStartNow is torrent-start-now: torrents/setForceStart value=true,
// which also starts stopped torrents (qBT 4.6.4 and 5.3.0).
func TorrentStartNow(args json.RawMessage) (JsonMap, error) {
	return torrentAction("torrent-start-now", args, func(hashes []qBT.Hash) error {
		return qBTConn.SetForceStart(hashes, true)
	})
}
