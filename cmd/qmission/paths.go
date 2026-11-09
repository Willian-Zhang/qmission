package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"

	log "github.com/sirupsen/logrus"
)

func init() {
	registerRPC("torrent-set-location", TorrentSetLocationV2)
	registerRPC("torrent-rename-path", TorrentRenamePath)
}

var errMoveFalse = errors.New("torrent-set-location: qBittorrent always moves the data; move=false isn't supported (tick \"move data\")")

// TorrentSetLocationV2 is torrent-set-location (rpc-spec 3.6) via
// torrents/setLocation. qBT always moves the data and turns automatic torrent
// management off for the torrent. `move` defaults to false in the spec
// ("search location for files"), which qBT can't do, so move=false only
// succeeds (as a no-op) when every selected torrent is already there.
func TorrentSetLocationV2(args json.RawMessage) (JsonMap, error) {
	var req struct {
		Ids      *json.RawMessage `json:"ids"`
		Location *string          `json:"location"`
		Move     json.RawMessage  `json:"move"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, fmt.Errorf("torrent-set-location: invalid arguments: %w", err)
	}
	if req.Location == nil {
		return nil, errors.New("torrent-set-location: no location")
	}
	location := strings.TrimSpace(*req.Location) // qBT trims it too
	if location == "" {
		return nil, errors.New("torrent-set-location: location is empty")
	}
	if !isAbsolutePath(location) {
		return nil, errors.New("torrent-set-location: new location path is not absolute")
	}
	move, _, err := parseBoolArg(req.Move, "move")
	if err != nil {
		return nil, fmt.Errorf("torrent-set-location: %w", err)
	}

	sel, err := parseIDsField(req.Ids)
	if err != nil {
		return nil, err
	}
	if len(sel.torrents) == 0 {
		return JsonMap{}, nil
	}
	if !move {
		for _, t := range sel.torrents {
			if !samePath(t.Save_path, location) {
				return nil, errMoveFalse
			}
		}
		return JsonMap{}, nil
	}
	log.WithField("count", len(sel.torrents)).Info("Moving torrents")
	if err := qBTConn.SetLocation(sel.torrents.Hashes(), location); err != nil {
		return nil, fmt.Errorf("torrent-set-location: %w", err)
	}
	return JsonMap{}, nil
}

var windowsAbsPath = regexp.MustCompile(`^([A-Za-z]:[\\/]|\\\\)`)

// isAbsolutePath accepts POSIX and Windows absolute paths (qBT may run on
// either). Transmission rejects relative locations; qBT would resolve them
// against its default save path.
func isAbsolutePath(p string) bool {
	return strings.HasPrefix(p, "/") || windowsAbsPath.MatchString(p)
}

func samePath(a, b string) bool {
	norm := func(p string) string {
		return path.Clean(strings.ReplaceAll(strings.TrimSpace(p), `\`, "/"))
	}
	return norm(a) == norm(b)
}

// fileSystemSafeName is TrguiNG's (src/trutil.ts): it sends the torrent name
// through it as `path` when renaming a torrent, and uses it as the file tree's
// root path.
var trguingBadChars = regexp.MustCompile(`[<>:"\\/|?*]`)

func fileSystemSafeName(name string) string {
	if name == "" {
		return "_"
	}
	return trguingBadChars.ReplaceAllString(name, "_")
}

var (
	errRenameNoPath     = errors.New("torrent-rename-path: no path")
	errRenameBadName    = errors.New(`torrent-rename-path: invalid new name (empty, ".", "..", or contains "/")`)
	errRenameNotFound   = errors.New("torrent-rename-path: path not found in the torrent's files")
	errRenameExists     = errors.New("torrent-rename-path: a file or folder with the new name already exists")
	errRenameOneTorrent = errors.New("torrent-rename-path requires 1 torrent")
)

// renamePlan is what torrent-rename-path does in qBT terms.
type renamePlan struct {
	oldPath, newPath string // on-disk rename; empty when nothing moves
	folder           bool   // renameFolder instead of renameFile
	renameTorrent    bool   // also set the display name (torrents/rename)
}

// planRename resolves Transmission's `path` against qBT's file list (names
// relative to the save path, "/"-separated) the way tr_torrentRenamePath does:
// `path` names a file or a folder prefix, the new path is its parent plus
// `newName`, and renaming a top-level entry that covers every file also
// renames the torrent.
//
// Extra cases for qBT and TrguiNG:
//   - `path` equal to the torrent name (or TrguiNG's fileSystemSafeName of it)
//     with no match on disk is the torrent root: the single file, or the one
//     top folder shared by every file, plus the display name. qBT's display
//     name can differ from the folder (renamed earlier, other content layout),
//     and without metadata (no files) only the display name changes.
//   - TrguiNG builds folder paths from fileSystemSafeName(name) as the root, so
//     a leading component equal to it is dropped when the path doesn't match
//     as given.
func planRename(torrentName string, files []string, oldPath, newName string) (renamePlan, error) {
	if newName == "" || newName == "." || newName == ".." || strings.Contains(newName, "/") {
		return renamePlan{}, errRenameBadName
	}
	oldPath = strings.TrimSuffix(oldPath, "/")
	if oldPath == "" {
		return renamePlan{}, errRenameNoPath
	}
	isName := func(p string) bool { return p == torrentName || p == fileSystemSafeName(torrentName) }

	candidates := []string{oldPath}
	if i := strings.IndexByte(oldPath, '/'); i > 0 && isName(oldPath[:i]) {
		candidates = append(candidates, oldPath[i+1:])
	}
	var plan renamePlan
	resolved, folder, affected := "", false, 0
	for _, c := range candidates {
		if folder, affected = matchRenamePath(files, c); affected > 0 {
			resolved = c
			break
		}
	}
	if resolved == "" {
		if strings.Contains(oldPath, "/") || !isName(oldPath) {
			return renamePlan{}, errRenameNotFound
		}
		plan.renameTorrent = true
		resolved, folder = torrentRoot(files)
		if resolved == "" {
			return plan, nil // no files yet, or no single top folder: display name only
		}
		affected = len(files)
	}

	newPath := newName
	if dir := path.Dir(resolved); dir != "." {
		newPath = dir + "/" + newName
	}
	if newPath != resolved {
		for _, f := range files {
			if f == newPath || strings.HasPrefix(f, newPath+"/") {
				return renamePlan{}, errRenameExists
			}
		}
		plan.oldPath, plan.newPath, plan.folder = resolved, newPath, folder
	}
	if !strings.Contains(resolved, "/") && affected == len(files) {
		plan.renameTorrent = true
	}
	return plan, nil
}

// matchRenamePath reports whether p is a file or a folder of the torrent, and
// how many files it covers (0: no match).
func matchRenamePath(files []string, p string) (folder bool, affected int) {
	for _, f := range files {
		switch {
		case f == p:
			return false, 1
		case strings.HasPrefix(f, p+"/"):
			affected++
		}
	}
	return affected > 0, affected
}

// torrentRoot is the entry holding all of the torrent's data: its only file
// when that sits at the top, or the top folder every file shares.
func torrentRoot(files []string) (root string, folder bool) {
	if len(files) == 1 && !strings.Contains(files[0], "/") {
		return files[0], false
	}
	for _, f := range files {
		i := strings.IndexByte(f, '/')
		if i <= 0 || (root != "" && f[:i] != root) {
			return "", false
		}
		root = f[:i]
	}
	return root, root != ""
}

// TorrentRenamePath is torrent-rename-path (rpc-spec 3.7): renameFile or
// renameFolder for the entry, plus torrents/rename when the torrent's root
// changes (see planRename). Answers `path`, `name` and `id` like Transmission.
func TorrentRenamePath(args json.RawMessage) (JsonMap, error) {
	var req struct {
		Ids  *json.RawMessage `json:"ids"`
		Path *string          `json:"path"`
		Name *string          `json:"name"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, fmt.Errorf("torrent-rename-path: invalid arguments: %w", err)
	}
	if req.Path == nil {
		return nil, errRenameNoPath
	}
	if req.Name == nil {
		return nil, errRenameBadName
	}
	sel, err := parseIDsField(req.Ids)
	if err != nil {
		return nil, err
	}
	if len(sel.torrents) != 1 {
		return nil, errRenameOneTorrent
	}
	t := sel.torrents[0]

	qbtFiles, err := qBTConn.GetPropsFiles(t.Hash)
	if err != nil {
		return nil, fmt.Errorf("torrent-rename-path: reading the file list: %w", err)
	}
	files := make([]string, len(qbtFiles))
	for i, f := range qbtFiles {
		files[i] = f.Name
	}
	plan, err := planRename(t.Name, files, *req.Path, *req.Name)
	if err != nil {
		return nil, err
	}
	log.WithField("id", t.Id).WithField("folder", plan.folder).WithField("onDisk", plan.oldPath != "").
		WithField("torrentName", plan.renameTorrent).Info("Renaming torrent path")

	if plan.oldPath != "" {
		rename := qBTConn.RenameFile
		if plan.folder {
			rename = qBTConn.RenameFolder
		}
		if err := rename(t.Hash, plan.oldPath, plan.newPath); err != nil {
			return nil, fmt.Errorf("torrent-rename-path: %w", err)
		}
	}
	if plan.renameTorrent && t.Name != *req.Name {
		if err := qBTConn.RenameTorrent(t.Hash, *req.Name); err != nil {
			if plan.oldPath != "" {
				return nil, fmt.Errorf("torrent-rename-path: renamed on disk, but setting the torrent name failed: %w", err)
			}
			return nil, fmt.Errorf("torrent-rename-path: %w", err)
		}
	}
	return JsonMap{"path": EscapeString(*req.Path), "name": EscapeString(*req.Name), "id": t.Id}, nil
}
