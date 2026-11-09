package main

import (
	"net/url"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/h2non/gock.v1"
)

// trguingSafeName is TrguiNG's fileSystemSafeName (src/trutil.ts), written
// independently of the code under test.
func trguingSafeName(name string) string {
	if name == "" {
		return "_"
	}
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"\/|?*`, r) {
			return '_'
		}
		return r
	}, name)
}

// recordedFiles maps each recorded torrent's hash to its qBT file names, in
// torrents/files order.
func recordedFiles(t *testing.T, ds *dataset) map[string][]string {
	t.Helper()
	re := regexp.MustCompile(`torrent_(\d+)_properties\.json$`)
	props, _ := filepath.Glob(filepath.Join(ds.dir, "torrent_*_properties.json"))
	out := map[string][]string{}
	for _, p := range props {
		var prop map[string]interface{}
		readJSONFile(t, p, &prop)
		var files []map[string]interface{}
		readJSONFile(t, filepath.Join(ds.dir, "torrent_"+re.FindStringSubmatch(p)[1]+"_files.json"), &files)
		names := make([]string, len(files))
		for i, f := range files {
			names[i] = f["name"].(string)
		}
		out[prop["hash"].(string)] = names
	}
	return out
}

func idOf(t *testing.T, byID map[float64]string, hash string) float64 {
	t.Helper()
	for id, h := range byID {
		if h == hash {
			return id
		}
	}
	t.Fatalf("hash %s not in the torrent table", hash)
	return 0
}

func firstID(byID map[float64]string) float64 {
	first := 1e9
	for id := range byID {
		if id < first {
			first = id
		}
	}
	return first
}

// newSince returns a function reporting the forms each capture got since its last call.
func newSince(caps map[string]*captured) func() map[string][]url.Values {
	seen := map[string]int{}
	return func() map[string][]url.Values {
		out := map[string][]url.Values{}
		for name, c := range caps {
			all := c.all()
			if len(all) > seen[name] {
				out[name] = all[seen[name]:]
			}
			seen[name] = len(all)
		}
		return out
	}
}

func TestPlanRename(t *testing.T) {
	multi := []string{"Top/a.mkv", "Top/sub/b.srt", "Top/sub/c.srt", "Top/d.txt"}
	tests := []struct {
		name, torrent string
		files         []string
		path, newName string
		want          renamePlan
		err           error
	}{
		{"file in folder", "Top", multi, "Top/a.mkv", "x.mkv", renamePlan{oldPath: "Top/a.mkv", newPath: "Top/x.mkv"}, nil},
		{"subfolder", "Top", multi, "Top/sub", "subs", renamePlan{oldPath: "Top/sub", newPath: "Top/subs", folder: true}, nil},
		{"subfolder trailing slash", "Top", multi, "Top/sub/", "subs", renamePlan{oldPath: "Top/sub", newPath: "Top/subs", folder: true}, nil},
		{"top folder renames the torrent", "Top", multi, "Top", "New", renamePlan{oldPath: "Top", newPath: "New", folder: true, renameTorrent: true}, nil},
		{"display name differs from folder", "Shown", multi, "Shown", "New", renamePlan{oldPath: "Top", newPath: "New", folder: true, renameTorrent: true}, nil},
		{"TrguiNG safe name as root", `A "b"`, []string{`A "b"/1`, `A "b"/2`}, "A _b_", "New",
			renamePlan{oldPath: `A "b"`, newPath: "New", folder: true, renameTorrent: true}, nil},
		{"TrguiNG folder under safe root", `A "b"`, []string{`A "b"/s/1`, `A "b"/2`}, `A _b_/A "b"/s`, "t",
			renamePlan{oldPath: `A "b"/s`, newPath: `A "b"/t`, folder: true}, nil},
		{"safe root prefix when folder matches", "Top", multi, "Top/sub", "subs", renamePlan{oldPath: "Top/sub", newPath: "Top/subs", folder: true}, nil},
		{"single file", "f.iso", []string{"f.iso"}, "f.iso", "g.iso", renamePlan{oldPath: "f.iso", newPath: "g.iso", renameTorrent: true}, nil},
		{"single file, name without extension", `F "1"`, []string{`F "1".iso`}, "F _1_", "G.iso",
			renamePlan{oldPath: `F "1".iso`, newPath: "G.iso", renameTorrent: true}, nil},
		{"no metadata: display name only", "magnet", nil, "magnet", "m2", renamePlan{renameTorrent: true}, nil},
		{"no common root (NoSubfolder layout)", "T", []string{"a", "b"}, "T", "U", renamePlan{renameTorrent: true}, nil},
		{"top-level file of several", "T", []string{"a", "b"}, "a", "c", renamePlan{oldPath: "a", newPath: "c"}, nil},
		{"same name is a no-op", "Top", multi, "Top/a.mkv", "a.mkv", renamePlan{}, nil},
		{"same root name only renames the torrent", "Shown", multi, "Top", "Top", renamePlan{renameTorrent: true}, nil},
		{"target file exists", "Top", multi, "Top/a.mkv", "d.txt", renamePlan{}, errRenameExists},
		{"target folder exists", "Top", multi, "Top/d.txt", "sub", renamePlan{}, errRenameExists},
		{"not found", "Top", multi, "Top/nope", "x", renamePlan{}, errRenameNotFound},
		{"not found at root", "Top", multi, "Other", "x", renamePlan{}, errRenameNotFound},
		{"prefix is not a folder", "Top", multi, "Top/su", "x", renamePlan{}, errRenameNotFound},
		{"name with slash", "Top", multi, "Top/a.mkv", "x/y", renamePlan{}, errRenameBadName},
		{"name dot-dot", "Top", multi, "Top/a.mkv", "..", renamePlan{}, errRenameBadName},
		{"empty name", "Top", multi, "Top/a.mkv", "", renamePlan{}, errRenameBadName},
		{"empty path", "Top", multi, "", "x", renamePlan{}, errRenameNoPath},
	}
	for _, tt := range tests {
		got, err := planRename(tt.torrent, tt.files, tt.path, tt.newName)
		if err != tt.err || (err == nil && got != tt.want) {
			t.Errorf("%s: planRename = %+v, %v; want %+v, %v", tt.name, got, err, tt.want, tt.err)
		}
	}
}

func TestFileSystemSafeNameMatchesTrguiNG(t *testing.T) {
	for _, s := range []string{"", "plain", `a<b>c:d"e\f/g|h?i*j`, `Torrent 2 中文 🎬 "café"`} {
		if got, want := fileSystemSafeName(s), trguingSafeName(s); got != want {
			t.Errorf("fileSystemSafeName(%q) = %q, want %q", s, got, want)
		}
	}
}

// TestDatasetTorrentRenamePath renames the recorded torrents the way TrguiNG
// (and a Transmission-style client) asks: torrent root (single file, multi-file
// folder), a file, folders, no-ops and errors, checking each qBT call per version.
func TestDatasetTorrentRenamePath(t *testing.T) {
	caps := map[string]*captured{}
	var noMeta []string
	forEachDatasetWith(t, func(t *testing.T, ds *dataset) {
		caps["file"] = capturePost(t, "/api/v2/torrents/renameFile$", 200, "")
		caps["folder"] = capturePost(t, "/api/v2/torrents/renameFolder$", 200, "")
		caps["name"] = capturePost(t, "/api/v2/torrents/rename$", 200, "")
		// Torrents without a recorded file list are fetching metadata: no files.
		noMeta = nil
		for hash, tor := range ds.torrentsAfter(len(ds.syncFiles) - 1) {
			if _, ok := ds.props[hash]; !ok {
				if s, _ := tor["state"].(string); s == "metaDL" || s == "forcedMetaDL" {
					noMeta = append(noMeta, hash)
				}
				gock.New(testQBTAddr).Get("/api/v2/torrents/files").MatchParam("hash", "^"+hash+"$").Persist().Reply(200).BodyString("[]")
			}
		}
	}, func(t *testing.T, ds *dataset, c *rpcClient) {
		_, byID := drainSync(t, ds, c)
		table := ds.torrentsAfter(len(ds.syncFiles) - 1)
		files := recordedFiles(t, ds)
		since := newSince(caps)
		var singles, multis []string
		for hash, names := range files {
			if table[hash] == nil {
				continue
			}
			if len(names) == 1 && !strings.Contains(names[0], "/") {
				singles = append(singles, hash)
			} else if len(names) > 1 {
				multis = append(multis, hash)
			}
		}
		sort.Strings(singles)
		sort.Strings(multis)

		type want struct {
			file, folder [][2]string // oldPath, newPath
			name         string      // torrents/rename name, "" = no call
		}
		run := func(label, hash, reqPath, newName string, w want) {
			t.Helper()
			id := idOf(t, byID, hash)
			args := c.mustSucceed("torrent-rename-path", map[string]interface{}{"ids": []float64{id}, "path": reqPath, "name": newName})
			if args["path"] != reqPath || args["name"] != newName || args["id"] != id {
				t.Errorf("%s: response %v", label, args)
			}
			got := since()
			check := func(kind string, pairs [][2]string) {
				if len(got[kind]) != len(pairs) {
					t.Errorf("%s: %d %s calls, want %d", label, len(got[kind]), kind, len(pairs))
					return
				}
				for i, p := range pairs {
					expectForm(t, got[kind][i], map[string]string{"hash": hash, "oldPath": p[0], "newPath": p[1]})
				}
			}
			check("file", w.file)
			check("folder", w.folder)
			if w.name == "" {
				if len(got["name"]) != 0 {
					t.Errorf("%s: unexpected torrents/rename", label)
				}
			} else if len(got["name"]) != 1 {
				t.Errorf("%s: %d torrents/rename calls", label, len(got["name"]))
			} else {
				expectForm(t, got["name"][0], map[string]string{"hash": hash, "name": w.name})
			}
		}
		fails := func(label, wantErr string, args map[string]interface{}) {
			t.Helper()
			out := c.call("torrent-rename-path", args)
			if res, _ := out["result"].(string); !strings.Contains(res, wantErr) {
				t.Errorf("%s: result = %v, want %q", label, out["result"], wantErr)
			}
			if got := since(); len(got) != 0 {
				t.Errorf("%s: qBT calls %v", label, got)
			}
		}

		for _, hash := range singles {
			name := table[hash]["name"].(string)
			// TrguiNG's torrent-table rename: path = fileSystemSafeName(name).
			run("single root", hash, trguingSafeName(name), "Renamed single.iso",
				want{file: [][2]string{{files[hash][0], "Renamed single.iso"}}, name: "Renamed single.iso"})
			run("single file by its path", hash, files[hash][0], "Other.iso",
				want{file: [][2]string{{files[hash][0], "Other.iso"}}, name: "Other.iso"})
		}
		if len(singles) == 0 {
			t.Log("no single-file torrent with a file list in this recording")
		}
		if len(multis) == 0 {
			t.Fatal("no multi-file torrent with a file list in this recording")
		}
		for _, hash := range multis {
			name := table[hash]["name"].(string)
			names := files[hash]
			root := strings.SplitN(names[0], "/", 2)[0]
			run("multi root (TrguiNG)", hash, trguingSafeName(name), "Renamed root",
				want{folder: [][2]string{{root, "Renamed root"}}, name: "Renamed root"})
			run("multi top folder (Transmission)", hash, root, "Renamed root",
				want{folder: [][2]string{{root, "Renamed root"}}, name: "Renamed root"})
			if safe := trguingSafeName(name); safe != root {
				// TrguiNG's file tree puts the real root under the safe name.
				run("multi top folder (TrguiNG tree)", hash, safe+"/"+root, "Renamed root",
					want{folder: [][2]string{{root, "Renamed root"}}, name: "Renamed root"})
			}
			run("file", hash, names[1], "renamed file.srt",
				want{file: [][2]string{{names[1], root + "/renamed file.srt"}}})
			run("file, same name", hash, names[1], path.Base(names[1]), want{})

			id := idOf(t, byID, hash)
			fails("existing target", "already exists", map[string]interface{}{"ids": []float64{id}, "path": names[1], "name": path.Base(names[2])})
			fails("unknown path", "not found", map[string]interface{}{"ids": []float64{id}, "path": root + "/no such file", "name": "x"})
			fails("bad name", "invalid new name", map[string]interface{}{"ids": []float64{id}, "path": names[1], "name": "a/b"})
			fails("no path", "no path", map[string]interface{}{"ids": []float64{id}, "name": "x"})
		}
		for _, hash := range noMeta {
			name := table[hash]["name"].(string)
			run("no metadata", hash, trguingSafeName(name), "Renamed magnet", want{name: "Renamed magnet"})
		}
		fails("two torrents", "requires 1 torrent", map[string]interface{}{"ids": []float64{1, 2}, "path": "x", "name": "y"})
		fails("unknown id", "requires 1 torrent", map[string]interface{}{"ids": []float64{99999}, "path": "x", "name": "y"})
		fails("no ids (all)", "requires 1 torrent", map[string]interface{}{"path": "x", "name": "y"})
	})
}

// A 409 from renameFile/renameFolder (target exists on disk, invalid name)
// becomes a clear error without qBT's message, and the display name stays.
func TestDatasetRenameRejectedByQBT(t *testing.T) {
	var name *captured
	forEachDatasetWith(t, func(t *testing.T, ds *dataset) {
		for _, ep := range []string{"renameFile", "renameFolder"} {
			gock.New(testQBTAddr).Post("/api/v2/torrents/" + ep + "$").Persist().Reply(409).BodyString("The folder already exists: 'x'.")
		}
		name = capturePost(t, "/api/v2/torrents/rename$", 200, "")
	}, func(t *testing.T, ds *dataset, c *rpcClient) {
		_, byID := drainSync(t, ds, c)
		files := recordedFiles(t, ds)
		var hash string
		for h, names := range files {
			if len(names) > 1 {
				hash = h
			}
		}
		if hash == "" {
			t.Fatal("no multi-file torrent in this recording")
		}
		root := strings.SplitN(files[hash][0], "/", 2)[0]
		for _, p := range []string{root, files[hash][1]} {
			out := c.call("torrent-rename-path", map[string]interface{}{"ids": []float64{idOf(t, byID, hash)}, "path": p, "name": "Taken"})
			res, _ := out["result"].(string)
			if !strings.Contains(res, "refused the rename") || !strings.Contains(res, "409") || strings.Contains(res, "already exists: 'x'") {
				t.Errorf("path %d: result = %q", len(p), res)
			}
		}
		if name.count() != 0 {
			t.Error("torrents/rename called after a failed folder rename")
		}
	})
}

func TestDatasetTorrentSetLocation(t *testing.T) {
	var setLoc *captured
	forEachDatasetWith(t, func(t *testing.T, ds *dataset) {
		gock.New(testQBTAddr).Post("/api/v2/torrents/setLocation$").MatchType("url").BodyString("location=%2Fcannot").
			Persist().Reply(409).BodyString("Cannot make save path")
		setLoc = capturePost(t, "/api/v2/torrents/setLocation$", 200, "")
	}, func(t *testing.T, ds *dataset, c *rpcClient) {
		hashes, byID := drainSync(t, ds, c)
		table := ds.torrentsAfter(len(ds.syncFiles) - 1)
		first := firstID(byID)
		firstSave := table[byID[first]]["save_path"].(string)
		seen := 0
		calls := func() []url.Values {
			all := setLoc.all()
			out := all[seen:]
			seen = len(all)
			return out
		}

		// TrguiNG's Move dialog with "Move torrent data" ticked (the default).
		c.mustSucceed("torrent-set-location", map[string]interface{}{"ids": []interface{}{first, 99999}, "location": "/new/place", "move": true})
		c.mustSucceed("torrent-set-location", map[string]interface{}{"location": " /new/all ", "move": 1})
		c.mustSucceed("torrent-set-location", map[string]interface{}{"ids": []float64{first}, "location": `D:\Media`, "move": true})
		c.mustSucceed("torrent-set-location", map[string]interface{}{"ids": []float64{99999}, "location": "/x", "move": true})
		got := calls()
		if len(got) != 3 {
			t.Fatalf("%d setLocation calls, want 3", len(got))
		}
		expectForm(t, got[0], map[string]string{"hashes": byID[first], "location": "/new/place"})
		expectForm(t, got[1], map[string]string{"hashes": strings.Join(hashes, "|"), "location": "/new/all"})
		expectForm(t, got[2], map[string]string{"hashes": byID[first], "location": `D:\Media`})

		// move=false (or absent, the spec default) can't be done unless nothing moves.
		c.mustSucceed("torrent-set-location", map[string]interface{}{"ids": []float64{first}, "location": firstSave + "/", "move": false})
		allSame := true
		for _, tor := range table {
			if tor["save_path"] != firstSave {
				allSame = false
			}
		}
		out := c.call("torrent-set-location", map[string]interface{}{"location": firstSave, "move": false})
		if (out["result"] == "success") != allSame {
			t.Errorf("move=false to the first save path, all torrents (same=%v): %v", allSame, out["result"])
		}
		for label, args := range map[string]map[string]interface{}{
			"move=false":    {"ids": []float64{first}, "location": "/elsewhere", "move": false},
			"move absent":   {"ids": []float64{first}, "location": "/elsewhere"},
			"move=0":        {"ids": []float64{first}, "location": "/elsewhere", "move": 0},
			"relative":      {"ids": []float64{first}, "location": "downloads/x", "move": true},
			"empty":         {"ids": []float64{first}, "location": "  ", "move": true},
			"no location":   {"ids": []float64{first}, "move": true},
			"move a string": {"ids": []float64{first}, "location": "/x", "move": "yes"},
		} {
			res, _ := c.call("torrent-set-location", args)["result"].(string)
			if res == "success" {
				t.Errorf("%s: expected an error", label)
			}
			if strings.HasPrefix(label, "move") && label != "move a string" && !strings.Contains(res, "move=false isn't supported") {
				t.Errorf("%s: result = %q", label, res)
			}
		}
		if got := calls(); len(got) != 0 {
			t.Errorf("rejected requests reached qBT: %v", got)
		}

		res, _ := c.call("torrent-set-location", map[string]interface{}{"ids": []float64{first}, "location": "/cannot", "move": true})["result"].(string)
		if !strings.Contains(res, "couldn't create the new location") {
			t.Errorf("409: result = %q", res)
		}
	})
}

// Sanity check that the handlers above replaced the old main.go entries.
func TestPathHandlersRegistered(t *testing.T) {
	for method, h := range map[string]rpcHandler{
		"torrent-set-location": TorrentSetLocationV2,
		"torrent-rename-path":  TorrentRenamePath,
	} {
		if reflect.ValueOf(rpcMethods[method]).Pointer() != reflect.ValueOf(h).Pointer() {
			t.Errorf("%s is not served by the handler from paths.go", method)
		}
	}
}
