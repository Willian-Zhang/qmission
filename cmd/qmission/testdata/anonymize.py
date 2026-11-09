#!/usr/bin/env python3
"""Anonymize a qBittorrent Web API capture for qmission's testdata.

Reads records from stdin, one per line (written by test_data_fetcher.sh):

    <kind> TAB <real hash or -> TAB <output name> TAB <base64 body>

and writes the anonymized body to OUTDIR/<output name>. The real->fake mapping
lives only in memory, so one run gives consistent fake values across all files
of a capture. Numbers, booleans and states are never touched.

Never prints real values: errors report only the record kind and the
exception type.

    anonymize.py --rename-only <dir>...

rewrites already-anonymized files in place from the old ASCII fake names
(`Torrent <n>`, `Torrent <n>/file <m>.<ext>`) to the current ones (see
fake_name/fake_file). Idempotent; touches only strings in the anonymizer's
own fake-name forms, and reports any other `Torrent <n>` it can't place.
"""

import base64
import json
import os
import re
import sys
import urllib.parse

HEX40 = re.compile(r"\b[0-9a-fA-F]{40}\b")
HEX64 = re.compile(r"\b[0-9a-fA-F]{64}\b")
URL = re.compile(r"\b(?:https?|udp|wss?)://[^\s\"'<>]+")
IPV4 = re.compile(r"\b(?:\d{1,3}\.){3}\d{1,3}\b")
EXT = re.compile(r"^\.[A-Za-z0-9]{1,5}$")

# Fake names deliberately carry non-ASCII and JSON-special text so tests catch
# encoding bugs: CJK (BMP, 3-byte UTF-8), an emoji outside the BMP (4-byte
# UTF-8, UTF-16 surrogate pair), a combining mark (e + U+0301), a precomposed
# Latin-1 letter (U+00E9, 2-byte UTF-8) and a double quote. Every 4th torrent
# (n = 1, 5, 9, ...) keeps a plain ASCII name so both kinds are covered.
NAME_SUFFIX = ' \u4e2d\u6587 \U0001F3AC "cafe\u0301"'
FILE_SUFFIX = " caf\u00e9"


def fake_name(n):
    return "Torrent %d" % n if n % 4 == 1 else "Torrent %d%s" % (n, NAME_SUFFIX)


def fake_file(n, m, ext, nested):
    """File name inside torrent n; the root folder is the torrent name."""
    if nested:
        return "%s/file %d%s%s" % (fake_name(n), m, FILE_SUFFIX, ext)
    return fake_name(n) + ext


class Mapper:
    def __init__(self):
        self.hashes = {}      # real v1 (lowercase) -> n
        self.hashes_v2 = {}   # real v2 -> n
        self.fake_v1 = set()
        self.fake_v2 = set()
        self.paths = {}
        self.trackers = {}
        self.ips = {}
        self.tags = {}
        self.categories = {}
        self.clients = {}
        self.files = {}       # n -> {real file name -> m}

    # --- hashes -----------------------------------------------------------
    def index(self, h):
        h = h.lower()
        if h in self.fake_v1:
            return int(h, 16)
        if h not in self.hashes:
            n = len(self.hashes) + 1
            self.hashes[h] = n
            self.fake_v1.add(format(n, "040x"))
        return self.hashes[h]

    def hash(self, h):
        if not h:
            return h
        return format(self.index(h), "040x")

    def hash_v2(self, h):
        if not h:
            return h
        h = h.lower()
        if h in self.fake_v2:
            return h
        if h not in self.hashes_v2:
            self.hashes_v2[h] = len(self.hashes_v2) + 1
            self.fake_v2.add(format(self.hashes_v2[h], "064x"))
        return format(self.hashes_v2[h], "064x")

    # --- simple dictionaries ----------------------------------------------
    @staticmethod
    def _get(table, key, make):
        if key not in table:
            table[key] = make(len(table) + 1)
        return table[key]

    def tag(self, t):
        return self._get(self.tags, t, lambda k: "tag%d" % k) if t else t

    def tag_list(self, s):
        if not s:
            return s
        return ", ".join(self.tag(t.strip()) for t in s.split(",") if t.strip())

    def category(self, c):
        return self._get(self.categories, c, lambda k: "category%d" % k) if c else c

    def client(self, c):
        return self._get(self.clients, c, lambda k: "Client %d" % k) if c else c

    def ip(self, a):
        if not a or a.startswith("10.99."):
            return a
        return self._get(self.ips, a, lambda k: "10.99.%d.%d" % (k // 254, k % 254 + 1))

    def tracker(self, u):
        if not u or u.startswith("** ["):
            return u
        if ".example/" in u:
            return u
        scheme = u.split("://", 1)[0].lower() if "://" in u else "http"
        if scheme not in ("http", "https", "udp", "ws", "wss"):
            scheme = "http"
        k = self._get(self.trackers, u, lambda k: k)
        return "%s://tracker%d.example/announce" % (scheme, k)

    def path(self, p):
        if not p or p.startswith("/downloads"):
            return p
        slash = p.endswith("/") or p.endswith("\\")
        k = self._get(self.paths, p.rstrip("/\\"), lambda k: k)
        out = "/downloads" if k == 1 else "/downloads/dir%d" % k
        return out + "/" if slash else out

    # --- names ------------------------------------------------------------
    def name(self, n):
        return fake_name(n)

    def file(self, n, real):
        table = self.files.setdefault(n, {})
        if real not in table:
            table[real] = len(table) + 1
        m = table[real]
        last = re.split(r"[/\\]", real)[-1]
        ext = os.path.splitext(last)[1]
        ext = ext if EXT.match(ext) else ""
        return fake_file(n, m, ext, "/" in real or "\\" in real)

    def content_path(self, n, real, save_real):
        if not real:
            return real
        if save_real and real.rstrip("/") == save_real.rstrip("/"):
            return self.path(real)
        parent, last = os.path.split(real.rstrip("/"))
        ext = os.path.splitext(last)[1]
        ext = ext if EXT.match(ext) else ""
        return "%s/%s%s" % (self.path(parent).rstrip("/"), fake_name(n), ext)

    def magnet(self, n, real):
        if not real:
            return real
        q = urllib.parse.parse_qs(real.split("?", 1)[1] if "?" in real else "")
        parts = ["xt=urn:btih:" + format(n, "040x"),
                 "dn=" + urllib.parse.quote(fake_name(n), safe="")]
        for tr in q.get("tr", []):
            parts.append("tr=" + urllib.parse.quote(self.tracker(tr), safe=""))
        return "magnet:?" + "&".join(parts)

    # --- catch-all safety net ---------------------------------------------
    def scrub(self, s):
        if not isinstance(s, str):
            return s
        s = URL.sub(lambda m: self.tracker(m.group(0)), s)
        s = HEX64.sub(lambda m: self.hash_v2(m.group(0)), s)
        s = HEX40.sub(lambda m: self.hash(m.group(0)), s)
        s = IPV4.sub(lambda m: self.ip(m.group(0)), s)
        return s

    def deep_scrub(self, v):
        if isinstance(v, dict):
            return {self.scrub(k): self.deep_scrub(x) for k, x in v.items()}
        if isinstance(v, list):
            return [self.deep_scrub(x) for x in v]
        return self.scrub(v)


M = Mapper()

COMMENT = "anonymized comment"
CREATOR = "anonymized creator"


def torrent_fields(t, n):
    """Torrent object from torrents/info, maindata or properties (in place)."""
    save_real = t.get("save_path")
    for k, v in list(t.items()):
        if not isinstance(v, str):
            continue
        if k in ("hash", "infohash_v1"):
            t[k] = M.hash(v)
        elif k == "infohash_v2":
            t[k] = M.hash_v2(v)
        elif k == "name":
            t[k] = M.name(n)
        elif k == "magnet_uri":
            t[k] = M.magnet(n, v)
        elif k in ("content_path", "root_path"):
            t[k] = M.content_path(n, v, save_real)
        elif k in ("save_path", "download_path"):
            t[k] = M.path(v)
        elif k == "tracker":
            t[k] = M.tracker(v)
        elif k == "category":
            t[k] = M.category(v)
        elif k == "tags":
            t[k] = M.tag_list(v)
        elif k == "comment":
            t[k] = COMMENT if v else v
        elif k == "created_by":
            t[k] = CREATOR if v else v
    return t


def do_list(body, _h):
    for t in body:
        torrent_fields(t, M.index(t["hash"]))
    return body


def do_maindata(body, _h):
    torrents = body.get("torrents")
    if isinstance(torrents, dict):
        body["torrents"] = {M.hash(h): torrent_fields(t, M.index(h)) for h, t in torrents.items()}
    if isinstance(body.get("torrents_removed"), list):
        body["torrents_removed"] = [M.hash(h) for h in body["torrents_removed"]]
    cats = body.get("categories")
    if isinstance(cats, dict):
        out = {}
        for c, v in cats.items():
            if isinstance(v, dict):
                v = dict(v)
                if "name" in v:
                    v["name"] = M.category(v["name"])
                for pk in ("savePath", "download_path", "downloadPath"):
                    if isinstance(v.get(pk), str):
                        v[pk] = M.path(v[pk])
            out[M.category(c)] = v
        body["categories"] = out
    if isinstance(body.get("categories_removed"), list):
        body["categories_removed"] = [M.category(c) for c in body["categories_removed"]]
    for k in ("tags", "tags_removed"):
        if isinstance(body.get(k), list):
            body[k] = [M.tag(t) for t in body[k]]
    trackers = body.get("trackers")
    if isinstance(trackers, dict):
        body["trackers"] = {M.tracker(u): [M.hash(h) for h in hs] for u, hs in trackers.items()}
    if isinstance(body.get("trackers_removed"), list):
        body["trackers_removed"] = [M.tracker(u) for u in body["trackers_removed"]]
    ss = body.get("server_state")
    if isinstance(ss, dict):
        if ss.get("last_external_address_v4"):
            ss["last_external_address_v4"] = "10.99.255.254"
        if ss.get("last_external_address_v6"):
            ss["last_external_address_v6"] = "fd00::99"
    return body


def do_properties(body, h):
    return torrent_fields(body, M.index(h))


def do_trackers(body, _h):
    for t in body:
        if isinstance(t.get("url"), str):
            t["url"] = M.tracker(t["url"])
        # 5.x per-interface endpoints are named "<local ip>:<listen port>".
        for e in t.get("endpoints") or []:
            if isinstance(e, dict) and isinstance(e.get("name"), str):
                addr, _, _port = e["name"].rpartition(":")
                e["name"] = "%s:%d" % (M.ip(addr.strip("[]")), FAKE_PORTS["listen_port"])
    return body


def do_files(body, h):
    n = M.index(h)
    for f in body:
        if isinstance(f.get("name"), str):
            f["name"] = M.file(n, f["name"])
    return body


def do_peers(body, h):
    n = M.index(h)
    peers = body.get("peers")
    if isinstance(peers, dict):
        out = {}
        for key, p in peers.items():
            addr, _, port = key.rpartition(":")
            fake = M.ip(addr.strip("[]"))
            if isinstance(p, dict):
                if "ip" in p:
                    p["ip"] = M.ip(p["ip"])
                if p.get("host_name"):
                    p["host_name"] = ""
                if isinstance(p.get("client"), str):
                    p["client"] = M.client(p["client"])
                if p.get("peer_id_client"):
                    p["peer_id_client"] = "-XX0000-"
                if isinstance(p.get("files"), str) and p["files"]:
                    p["files"] = "\n".join(M.file(n, f) for f in p["files"].split("\n"))
            out["%s:%s" % (fake, port)] = p
        body["peers"] = out
    if isinstance(body.get("peers_removed"), list):
        body["peers_removed"] = [
            "%s:%s" % (M.ip(k.rpartition(":")[0].strip("[]")), k.rpartition(":")[2])
            for k in body["peers_removed"]
        ]
    return body


PREF_SECRET = re.compile(
    r"pass|user|mail|smtp|proxy_ip|host|domain|path|dir|_ip|ips|address|iface|"
    r"interface|program|url|cookie|header|key|cert|whitelist|list|rss|trackers|"
    r"excluded|dyndns|bootstrap|stun|instance|sender|scan|banned|executable",
    re.I,
)


# The machine's real ports, replaced with fixed defaults.
FAKE_PORTS = {"listen_port": 6881, "web_ui_port": 8080, "ssl_listen_port": 6882}


def do_preferences(body, _h):
    for k, port in FAKE_PORTS.items():
        if isinstance(body.get(k), int) and body[k] != 0:
            body[k] = port
    for k, v in list(body.items()):
        if isinstance(v, str):
            if PREF_SECRET.search(k) or "/" in v or "@" in v or "://" in v:
                body[k] = ""
        elif isinstance(v, (dict, list)):
            body[k] = type(v)()
    return body


def do_passthrough(body, _h):
    return body


KINDS = {
    "list": do_list,
    "maindata": do_maindata,
    "properties": do_properties,
    "trackers": do_trackers,
    "files": do_files,
    "peers": do_peers,
    "preferences": do_preferences,
    "piecestates": do_passthrough,
}


# --- --rename-only: old fake names -> current ones --------------------------
OLD_EXT = r"(\.[A-Za-z0-9]{1,5})?"
OLD_FILE = re.compile(r"^Torrent (\d+)/file (\d+)%s$" % OLD_EXT)
OLD_SINGLE = re.compile(r"^Torrent (\d+)%s$" % OLD_EXT)
OLD_PATH = re.compile(r"^(/downloads(?:/[^/]*)*)/Torrent (\d+)%s$" % OLD_EXT)
OLD_DN = re.compile(r"([?&]dn=)Torrent%20(\d+)(?=&|$)")
ANY_NAME = re.compile(r"Torrent( |%20)(\d+)(?!\d)")


def count_old_names(s):
    """`Torrent <n>` occurrences not followed by fake_name(n)'s suffix."""
    left = 0
    for m in ANY_NAME.finditer(s):
        n = int(m.group(2))
        if n % 4 == 1:
            continue
        suffix = NAME_SUFFIX if m.group(1) == " " else urllib.parse.quote(NAME_SUFFIX, safe="")
        if not s.startswith(suffix, m.end()):
            left += 1
    return left


def rename_line(s):
    m = OLD_FILE.match(s)
    if m:
        return fake_file(int(m.group(1)), int(m.group(2)), m.group(3) or "", True)
    m = OLD_SINGLE.match(s)
    if m:
        return fake_name(int(m.group(1))) + (m.group(2) or "")
    m = OLD_PATH.match(s)
    if m:
        return "%s/%s%s" % (m.group(1), fake_name(int(m.group(2))), m.group(3) or "")
    if s.startswith("magnet:?"):
        return OLD_DN.sub(lambda m: m.group(1) + urllib.parse.quote(fake_name(int(m.group(2))), safe=""), s)
    return s


def rename_value(v, stats):
    if isinstance(v, dict):
        return {k: rename_value(x, stats) for k, x in v.items()}
    if isinstance(v, list):
        return [rename_value(x, stats) for x in v]
    if not isinstance(v, str):
        return v
    # peers' "files" is a newline-separated list of file names
    out = "\n".join(rename_line(line) for line in v.split("\n"))
    if out != v:
        stats["renamed"] += 1
    stats["unplaced"] += count_old_names(out)
    return out


def rename_only(dirs):
    status = 0
    for d in dirs:
        for fname in sorted(os.listdir(d)):
            if not fname.endswith(".json"):
                continue
            path = os.path.join(d, fname)
            with open(path, encoding="utf-8") as f:
                body = json.load(f)
            stats = {"renamed": 0, "unplaced": 0}
            body = rename_value(body, stats)
            with open(path, "w", encoding="utf-8") as f:
                json.dump(body, f, indent=1, ensure_ascii=False)
                f.write("\n")
            print("%s: %d strings renamed, %d old names left" % (path, stats["renamed"], stats["unplaced"]),
                  file=sys.stderr)
            if stats["unplaced"]:
                status = 1
    return status


def main():
    if len(sys.argv) > 2 and sys.argv[1] == "--rename-only":
        sys.exit(rename_only(sys.argv[2:]))
    outdir = sys.argv[1]
    os.makedirs(outdir, exist_ok=True)
    for line in sys.stdin:
        line = line.rstrip("\n")
        if not line:
            continue
        kind, real_hash, outname, b64 = (line.split("\t") + ["", "", "", ""])[:4]
        if real_hash != "-":
            outname = outname.replace("@N@", str(M.index(real_hash)))
        if not b64:
            print("skip %s: empty response" % outname, file=sys.stderr)
            continue
        raw = base64.b64decode(b64)
        path = os.path.join(outdir, outname)
        try:
            if kind == "text":
                text = M.scrub(raw.decode("utf-8"))
                with open(path, "w", encoding="utf-8") as f:
                    f.write(text)
            else:
                body = json.loads(raw)
                body = KINDS[kind](body, real_hash if real_hash != "-" else None)
                body = M.deep_scrub(body)
                with open(path, "w", encoding="utf-8") as f:
                    json.dump(body, f, indent=1, ensure_ascii=False)
                    f.write("\n")
        except Exception as e:  # never echo the payload
            print("error in %s (%s): %s" % (outname, kind, type(e).__name__), file=sys.stderr)
            continue
        print("wrote %s" % outname, file=sys.stderr)


if __name__ == "__main__":
    main()
