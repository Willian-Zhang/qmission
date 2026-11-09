#!/usr/bin/env python3
"""Consistency check for fake names in an anonymized capture dir.

    check_names.py <dir>...

For every torrent: one name across torrents/info, maindata and properties;
magnet `dn` decodes (UTF-8) to that name; content/root paths end in it;
files' root folder (or single-file stem) is it. Prints torrent numbers and
counts only. Exit 1 on any mismatch.
"""

import glob
import json
import os
import sys
import urllib.parse


def load(path):
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def check(d):
    bad = 0
    names = {}  # hash -> set of names

    def see(h, t, where):
        nonlocal bad
        if "name" in t:
            names.setdefault(h, set()).add(t["name"])
        name = t.get("name")
        mag = t.get("magnet_uri")
        if name and mag:
            q = urllib.parse.parse_qs(mag.split("?", 1)[1])
            if q.get("dn") != [name]:
                print("%s %d: magnet dn != name" % (where, int(h, 16)))
                bad += 1
        for k in ("content_path", "root_path"):
            p = t.get(k)
            if name and p and p != t.get("save_path", "").rstrip("/") and \
                    not os.path.basename(p).startswith(name):
                print("%s %d: %s doesn't end in name" % (where, int(h, 16), k))
                bad += 1

    for t in load(os.path.join(d, "torrent_list.json")):
        see(t["hash"], t, "torrent_list")
    for f in sorted(glob.glob(os.path.join(d, "sync_*.json"))):
        for h, t in (load(f).get("torrents") or {}).items():
            see(h, t, os.path.basename(f))
    for f in sorted(glob.glob(os.path.join(d, "torrent_*_properties.json"))):
        n = int(os.path.basename(f).split("_")[1])
        see(format(n, "040x"), load(f), os.path.basename(f))
    for h, s in names.items():
        if len(s) != 1:
            print("torrent %d: %d different names" % (int(h, 16), len(s)))
            bad += 1
    for f in sorted(glob.glob(os.path.join(d, "torrent_*_files.json"))):
        n = int(os.path.basename(f).split("_")[1])
        name = next(iter(names.get(format(n, "040x"), {""})))
        for e in load(f):
            fn = e["name"]
            ok = fn.split("/", 1)[0] == name if "/" in fn else fn.startswith(name)
            if not ok:
                print("%s: file root != torrent name" % os.path.basename(f))
                bad += 1
                break
    ascii_n = sum(1 for s in names.values() if all(ord(c) < 128 for c in next(iter(s))))
    print("%s: %d torrents (%d ASCII-named), %d problems" % (d, len(names), ascii_n, bad))
    return bad


if __name__ == "__main__":
    sys.exit(1 if sum(check(d) for d in sys.argv[1:]) else 0)
