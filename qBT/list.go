package qBT

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/iancoleman/orderedmap"
	log "github.com/sirupsen/logrus"
)

// TorrentsList is the torrent table plus the hash <-> ID mapping.
//
// IDs start at 1 and a hash keeps its ID for the life of the process, also when
// it disappears and comes back (e.g. a qBT restart). *TorrentInfo values handed
// out are never modified afterwards: updates replace the pointer, so callers
// can read them without holding the lock.
type TorrentsList struct {
	mutex       sync.RWMutex
	useSync     bool
	items       map[Hash]*TorrentInfo
	activity    map[Hash]time.Time
	deleted     map[ID]time.Time
	rid         int
	hashIds     map[ID]Hash // live torrents only
	idOf        map[Hash]ID // every hash seen, never shrinks
	lastIndex   ID          // last assigned ID
	serverState ServerState
}

type TorrentInfoList []*TorrentInfo

func (torrents TorrentInfoList) Len() int {
	return len(torrents)
}
func (torrents TorrentInfoList) Swap(i, j int) {
	torrents[i], torrents[j] = torrents[j], torrents[i]
}

func (torrents TorrentInfoList) Less(i, j int) bool {
	return torrents[i].Id < torrents[j].Id
}

func (torrents TorrentInfoList) ConcatenateHashes() string {
	hashesStrings := make([]string, len(torrents))
	for i, torrent := range torrents {
		hashesStrings[i] = string(torrent.Hash)
	}
	return strings.Join(hashesStrings, "|")
}

func (torrents TorrentInfoList) Hashes() []Hash {
	hashesStrings := make([]Hash, len(torrents))
	for i, torrent := range torrents {
		hashesStrings[i] = torrent.Hash
	}
	return hashesStrings
}

func (list *TorrentsList) reset(useSync bool) {
	list.mutex.Lock()
	defer list.mutex.Unlock()
	list.useSync = useSync
	list.items = make(map[Hash]*TorrentInfo)
	list.activity = make(map[Hash]time.Time)
	list.deleted = make(map[ID]time.Time)
	list.hashIds = make(map[ID]Hash)
	list.idOf = make(map[Hash]ID)
	list.rid = 0
	list.lastIndex = 0
	list.serverState = ServerState{}
}

// AllItems returns a copy of the table.
func (list *TorrentsList) AllItems() map[Hash]*TorrentInfo {
	list.mutex.RLock()
	defer list.mutex.RUnlock()
	result := make(map[Hash]*TorrentInfo, len(list.items))
	for hash, item := range list.items {
		result[hash] = item
	}
	return result
}

// ServerState is the merged sync/maindata server_state (zero without -sync).
func (list *TorrentsList) ServerState() ServerState {
	list.mutex.RLock()
	defer list.mutex.RUnlock()
	return list.serverState
}

func (list *TorrentsList) Slice() TorrentInfoList {
	list.mutex.RLock()
	defer list.mutex.RUnlock()

	result := make(TorrentInfoList, 0, len(list.items))
	for _, item := range list.items {
		result = append(result, item)
	}
	sort.Sort(result)
	return result
}

func (list *TorrentsList) GetActive() (resp TorrentInfoList) {
	list.mutex.RLock()
	defer list.mutex.RUnlock()

	for hash, item := range list.items {
		if activity, ok := list.activity[hash]; ok && time.Since(activity) < RECENTLY_ACTIVE_TIMEOUT {
			resp = append(resp, item)
		}
	}
	sort.Sort(resp)
	return
}

// GetRemoved returns the IDs removed within RECENTLY_ACTIVE_TIMEOUT, sorted.
func (list *TorrentsList) GetRemoved() []ID {
	list.mutex.Lock()
	defer list.mutex.Unlock()

	list.pruneDeleted()
	removed := make([]ID, 0, len(list.deleted))
	for id := range list.deleted {
		removed = append(removed, id)
	}
	sort.Slice(removed, func(i, j int) bool { return removed[i] < removed[j] })
	return removed
}

func (list *TorrentsList) ByID(id ID) *TorrentInfo {
	list.mutex.RLock()
	defer list.mutex.RUnlock()
	if hash, ok := list.hashIds[id]; ok {
		return list.items[hash]
	}
	return nil
}

func (list *TorrentsList) ByHash(hash Hash) *TorrentInfo {
	list.mutex.RLock()
	defer list.mutex.RUnlock()
	return list.items[hash]
}

func (list *TorrentsList) ItemsNum() int {
	list.mutex.RLock()
	defer list.mutex.RUnlock()
	return len(list.hashIds)
}

// assignID gives hash its ID (the old one if it had one). Needs the write lock.
func (list *TorrentsList) assignID(hash Hash) ID {
	id, known := list.idOf[hash]
	if !known {
		list.lastIndex++
		id = list.lastIndex
		list.idOf[hash] = id
		log.WithField("hash", hash).WithField("id", id).Debug("Torrent got assigned ID")
	}
	list.hashIds[id] = hash
	delete(list.deleted, id)
	return id
}

// remove drops hash from the table and records its ID as removed. Needs the write lock.
func (list *TorrentsList) remove(hash Hash) {
	if _, exists := list.items[hash]; !exists {
		return
	}
	id := list.idOf[hash]
	log.WithField("hash", hash).WithField("id", id).Info("Hash was removed from the torrent list")
	delete(list.items, hash)
	delete(list.activity, hash)
	delete(list.hashIds, id)
	list.pruneDeleted()
	list.deleted[id] = time.Now()
}

func (list *TorrentsList) pruneDeleted() {
	for id, removedAt := range list.deleted {
		if time.Since(removedAt) > RECENTLY_ACTIVE_TIMEOUT {
			delete(list.deleted, id)
		}
	}
}

// replaceAll swaps in a complete table (torrents/info). Every torrent counts as active.
func (list *TorrentsList) replaceAll(torrents TorrentInfoList) {
	list.mutex.Lock()
	defer list.mutex.Unlock()

	now := time.Now()
	newItems := make(map[Hash]*TorrentInfo, len(torrents))
	for _, torrent := range torrents {
		torrent.Id = list.assignID(torrent.Hash)
		newItems[torrent.Hash] = torrent
		list.activity[torrent.Hash] = now
	}
	for hash := range list.items {
		if _, still := newItems[hash]; !still {
			list.remove(hash)
		}
	}
	list.items = newItems
}

// applyMainData merges one sync/maindata answer. With full_update the torrent
// table and server_state are replaced, so torrents that vanished (e.g. across
// a qBT restart or a new qBT session) are removed. IDs are assigned in the
// order qBT lists the torrents.
func (list *TorrentsList) applyMainData(mainData *MainData) error {
	var keys []string
	var raw map[Hash]json.RawMessage
	if mainData.Torrents != nil {
		ordered := orderedmap.New()
		if err := json.Unmarshal(*mainData.Torrents, &ordered); err != nil {
			return err
		}
		keys = ordered.Keys()
		if err := json.Unmarshal(*mainData.Torrents, &raw); err != nil {
			return err
		}
	}

	list.mutex.Lock()
	defer list.mutex.Unlock()

	// Decode into copies first so a bad payload leaves the table untouched.
	updated := make([]*TorrentInfo, 0, len(keys))
	for _, key := range keys {
		hash := Hash(key)
		torrent := &TorrentInfo{}
		if old, exists := list.items[hash]; exists && !mainData.Full_update {
			*torrent = *old
		}
		if err := json.Unmarshal(raw[hash], torrent); err != nil {
			return err
		}
		torrent.Hash = hash
		updated = append(updated, torrent)
	}
	serverState := list.serverState
	if mainData.Full_update {
		serverState = ServerState{}
	}
	if mainData.Server_state != nil {
		if err := json.Unmarshal(*mainData.Server_state, &serverState); err != nil {
			return err
		}
	}

	list.rid = mainData.Rid
	list.serverState = serverState

	if mainData.Full_update {
		present := make(map[Hash]struct{}, len(updated))
		for _, torrent := range updated {
			present[torrent.Hash] = struct{}{}
		}
		for hash := range list.items {
			if _, still := present[hash]; !still {
				list.remove(hash)
			}
		}
	} else {
		for _, hash := range mainData.Torrents_removed {
			list.remove(hash)
		}
	}

	now := time.Now()
	for _, torrent := range updated {
		torrent.Id = list.assignID(torrent.Hash)
		list.items[torrent.Hash] = torrent
		list.activity[torrent.Hash] = now
	}
	return nil
}
