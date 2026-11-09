package transmission

import (
	"encoding/json"
)

type RPCRequest struct {
	Method    string
	Tag       json.RawMessage // echoed back as-is (a number in the spec)
	Arguments json.RawMessage
}

type GetRequest struct {
	Ids    *json.RawMessage
	Fields []string
	Format string // "objects" (default) or "table"
}

type TorrentAddRequest struct {
	Cookies           *string      //  pointer to a string of one or more cookies.
	Download_dir      *string      `json:"download-dir"` //    path to download the torrent to
	Filename          *string      //   filename or URL of the .torrent file
	Metainfo          *string      //   base64-encoded .torrent content
	Paused            *interface{} //    if true, don't start the torrent
	Peer_limit        *int         //   maximum number of peers
	BandwidthPriority *int         //   torrent's bandwidth tr_priority_t
	Files_wanted      *[]JsonMap   //   indices of file(s) to download
	Files_unwanted    *[]JsonMap   //    indices of file(s) to not download
	Priority_high     *[]JsonMap   //    indices of high-priority file(s)
	Priority_low      *[]JsonMap   //    indices of low-priority file(s)
	Priority_normal   *[]JsonMap   //    indices of normal-priority file(s)
}

// PeerInfo is one torrent-get `peers` entry (rpc-spec.md 4.1.4, old camelCase
// names). `peer_id`, `bytes_to_client` and `bytes_to_peer` are left out: qBT has
// no equivalent.
type PeerInfo struct {
	Address            string      `json:"address"`
	ClientName         interface{} `json:"clientName"`
	ClientIsChoked     bool        `json:"clientIsChoked"`
	ClientIsInterested bool        `json:"clientIsInterested"`
	FlagStr            string      `json:"flagStr"`
	IsDownloadingFrom  bool        `json:"isDownloadingFrom"`
	IsEncrypted        bool        `json:"isEncrypted"`
	IsIncoming         bool        `json:"isIncoming"`
	IsUploadingTo      bool        `json:"isUploadingTo"`
	IsUTP              bool        `json:"isUTP"`
	PeerIsChoked       bool        `json:"peerIsChoked"`
	PeerIsInterested   bool        `json:"peerIsInterested"`
	Port               int         `json:"port"`
	Progress           float64     `json:"progress"`     // 0..1
	RateToClient       int         `json:"rateToClient"` // B/s
	RateToPeer         int         `json:"rateToPeer"`   // B/s
}

// PeersFromTemplate: torrent-get `peersFrom` with every key the spec lists.
func PeersFromTemplate() JsonMap {
	return JsonMap{
		"fromCache": 0, "fromDht": 0, "fromIncoming": 0, "fromLpd": 0, "fromLtep": 0, "fromPex": 0, "fromTracker": 0,
	}
}
