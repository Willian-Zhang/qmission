package main

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"fmt"
	"github.com/jackpal/bencode-go"
	"io"
	"time"
)

// Structs into which torrent metafile is
// parsed and stored into.
type FileDict struct {
	Length int64    `bencode:"length"`
	Path   []string `bencode:"path"`
	Md5sum string   `bencode:"md5sum"`
}

type InfoDict struct {
	FileDuration []int64 `bencode:"file-duration"`
	FileMedia    []int64 `bencode:"file-media"`
	// Single file
	Name   string `bencode:"name"`
	Length int64  `bencode:"length"`
	Md5sum string `bencode:"md5sum"`
	// Multiple files
	Files       []FileDict `bencode:"files"`
	PieceLength int64      `bencode:"piece length"`
	Pieces      string     `bencode:"pieces"`
	Private     int64      `bencode:"private"`
	MetaVersion int64      `bencode:"meta version"` // 2 for v2 and hybrid torrents (BEP 52)
}

type MetaInfo struct {
	Info         InfoDict   `bencode:"info"`
	InfoHash     string     `bencode:"info hash"`    // raw SHA-1 of the info dict (v1 info hash)
	InfoHashV2   string     `bencode:"info hash v2"` // raw SHA-256 of the info dict, only for meta version 2
	Announce     string     `bencode:"announce"`
	AnnounceList [][]string `bencode:"announce-list"`
	CreationDate int64      `bencode:"creation date"`
	Comment      string     `bencode:"comment"`
	CreatedBy    string     `bencode:"created by"`
	Encoding     string     `bencode:"encoding"`
}

// Open .torrent file, un-bencode it and load them into MetaInfo struct.
func (metaInfo *MetaInfo) ReadTorrentMetaInfoFile(src io.Reader) bool {
	// Decode bencoded metainfo file.
	fileMetaData, er := bencode.Decode(src)
	if er != nil {
		return false
	}

	// fileMetaData is map of maps of... maps. Get top level map.
	metaInfoMap, ok := fileMetaData.(map[string]interface{})
	if !ok {
		return false
	}

	// Enumerate through child maps.
	for mapKey, mapVal := range metaInfoMap {
		switch mapKey {
		case "info":
			// The info hash is taken over the re-encoded info dict. bencode-go
			// writes dict keys sorted, so this equals the original bytes for
			// any canonical (BEP 3) .torrent.
			var bytesBuf bytes.Buffer
			if er = bencode.Marshal(&bytesBuf, mapVal); er != nil {
				return false
			}

			infoHash := sha1.Sum(bytesBuf.Bytes())
			metaInfo.InfoHash = string(infoHash[:])
			if info, ok := mapVal.(map[string]interface{}); ok {
				if version, _ := info["meta version"].(int64); version == 2 {
					infoHashV2 := sha256.Sum256(bytesBuf.Bytes())
					metaInfo.InfoHashV2 = string(infoHashV2[:])
				}
			}

			if er = bencode.Unmarshal(&bytesBuf, &metaInfo.Info); er != nil {
				return false
			}

		case "announce-list":
			var bytesBuf bytes.Buffer
			if er = bencode.Marshal(&bytesBuf, mapVal); er != nil {
				return false
			}
			if er = bencode.Unmarshal(&bytesBuf, &metaInfo.AnnounceList); er != nil {
				return false
			}

		case "announce":
			metaInfo.Announce, _ = mapVal.(string)

		case "creation date":
			metaInfo.CreationDate, _ = mapVal.(int64)

		case "comment":
			metaInfo.Comment, _ = mapVal.(string)

		case "created by":
			metaInfo.CreatedBy, _ = mapVal.(string)

		case "encoding":
			metaInfo.Encoding, _ = mapVal.(string)
		}
	}

	return metaInfo.InfoHash != ""
}

// Print torrent meta info struct data.
func (metaInfo *MetaInfo) DumpTorrentMetaInfo() {
	fmt.Println("Announce:", metaInfo.Announce)
	fmt.Println("Announce List:")
	for _, anncListEntry := range metaInfo.AnnounceList {
		for _, elem := range anncListEntry {
			fmt.Println("    ", elem)
		}
	}
	strCreationDate := time.Unix(metaInfo.CreationDate, 0)
	fmt.Println("Creation Date:", strCreationDate)
	fmt.Println("Comment:", metaInfo.Comment)
	fmt.Println("Created By:", metaInfo.CreatedBy)
	fmt.Println("Encoding:", metaInfo.Encoding)
	fmt.Printf("InfoHash: %X\n", metaInfo.InfoHash)
	fmt.Println("Info:")
	fmt.Println("    Piece Length:", metaInfo.Info.PieceLength)
	piecesList := metaInfo.getPiecesList()
	fmt.Printf("    Pieces:%X -- %X\n", len(piecesList), len(metaInfo.Info.Pieces)/20)
	fmt.Println("    File Duration:", metaInfo.Info.FileDuration)
	fmt.Println("    File Media:", metaInfo.Info.FileMedia)
	fmt.Println("    Private:", metaInfo.Info.Private)
	fmt.Println("    Name:", metaInfo.Info.Name)
	fmt.Println("    Length:", metaInfo.Info.Length)
	fmt.Println("    Md5sum:", metaInfo.Info.Md5sum)
	fmt.Println("    Files:")
	for _, fileDict := range metaInfo.Info.Files {
		fmt.Println("        Length:", fileDict.Length)
		fmt.Println("        Path:", fileDict.Path)
		fmt.Println("        Md5sum:", fileDict.Md5sum)
	}
}

// Splits pieces string into an array of 20 byte SHA1 hashes.
func (metaInfo *MetaInfo) getPiecesList() []string {
	var piecesList []string
	piecesLen := len(metaInfo.Info.Pieces)
	for i := 0; i+20 <= piecesLen; i += 20 {
		piecesList = append(piecesList, metaInfo.Info.Pieces[i:i+20])
	}
	return piecesList
}

// ParseMetainfo returns the hex v1 info hash, the hex v2 info hash ("" unless
// the torrent is v2 or hybrid) and the name of a .torrent.
func ParseMetainfo(metainfo []byte) (v1, v2, name string, err error) {
	var parsed MetaInfo
	if !parsed.ReadTorrentMetaInfoFile(bytes.NewReader(metainfo)) {
		return "", "", "", fmt.Errorf("invalid torrent metainfo")
	}
	v1 = fmt.Sprintf("%x", parsed.InfoHash)
	if parsed.InfoHashV2 != "" {
		v2 = fmt.Sprintf("%x", parsed.InfoHashV2)
	}
	return v1, v2, parsed.Info.Name, nil
}
