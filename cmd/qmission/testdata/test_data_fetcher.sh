#!/bin/sh
# Capture an anonymized qBittorrent Web API test set.
#
#   test_data_fetcher.sh <env file> <prefix> <output dir>
#   e.g. test_data_fetcher.sh scrachpad/qbt.env QBT53 cmd/qmission/testdata/5.3
#
# The env file must define <prefix>_URL (with trailing slash), <prefix>_USER and
# <prefix>_PASS. It is sourced, never printed. Raw responses are kept only in a
# private temp dir (deleted on exit) and go through anonymize.py before anything
# is written to <output dir>. Output: endpoint names, HTTP codes, and the chosen
# torrents' list indexes and states. No torrent data.
set -eu

[ $# -eq 3 ] || { echo "usage: $0 <env file> <prefix> <output dir>" >&2; exit 2; }
ENVFILE=$1
PREFIX=$2
OUTDIR=$3
HERE=$(cd "$(dirname "$0")" && pwd)

case $PREFIX in *[!A-Za-z0-9_]*|'') echo "bad prefix" >&2; exit 2 ;; esac
[ -f "$ENVFILE" ] || { echo "env file not found: $ENVFILE" >&2; exit 2; }
. "$ENVFILE"
eval "URL=\${${PREFIX}_URL:-}; QUSER=\${${PREFIX}_USER:-}; QPASS=\${${PREFIX}_PASS:-}"
[ -n "$URL" ] && [ -n "$QUSER" ] && [ -n "$QPASS" ] || { echo "${PREFIX}_URL/_USER/_PASS not all set" >&2; exit 2; }
case $URL in */) ;; *) URL="$URL/" ;; esac
API="${URL}api/v2"

umask 077
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT INT TERM
REC="$TMP/records"
: > "$REC"

code=$(curl -s -o "$TMP/login" -w '%{http_code}' -c "$TMP/jar" --referer "$URL" \
  --data-urlencode "username=$QUSER" --data-urlencode "password=$QPASS" "$API/auth/login")
QPASS=
# 4.x: 200 "Ok." / 200 "Fails."; 5.x: 204 empty body / 401 (authcontroller.cpp loginAction)
if ! { [ "$code" = 204 ] || { [ "$code" = 200 ] && grep -q '^Ok' "$TMP/login"; }; }; then
  echo "login failed: HTTP $code" >&2; exit 1
fi
echo "auth/login: HTTP $code"

# get <endpoint+query> <label> -> body in $TMP/body
get() {
  code=$(curl -s -o "$TMP/body" -w '%{http_code}' -b "$TMP/jar" --referer "$URL" "$API/$1")
  echo "$2: HTTP $code"
  [ "$code" = 200 ]
}

# rec <kind> <real hash or -> <output name>
rec() {
  printf '%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$(base64 < "$TMP/body" | tr -d '\n')" >> "$REC"
}

get app/version app/version && rec text - app_version.txt
get app/webapiVersion app/webapiVersion && rec text - webapi_version.txt
get app/preferences app/preferences && rec preferences - preferences.json

get 'sync/maindata?rid=0' 'sync/maindata rid=0' && rec maindata - sync_initial.json
rid=$(jq -r '.rid // 0' "$TMP/body")
for i in 1 2; do
  sleep 3
  get "sync/maindata?rid=$rid" "sync/maindata rid=$i" && rec maindata - "sync_$i.json"
  rid=$(jq -r '.rid // 0' "$TMP/body")
done

get torrents/info torrents/info && rec list - torrent_list.json
cp "$TMP/body" "$TMP/info"
echo "torrents: $(jq length "$TMP/info")"

# Pick up to 3 torrents by list index: downloading/stalled (prefer one with
# metadata), completed/seeding, stopped/paused.
jq -r '
  def first_idx(f): [to_entries[] | select(.value | f) | .key] | first // empty;
  [ first_idx((.state | test("^(downloading|stalledDL|forcedDL|queuedDL|checkingDL)$"))),
    first_idx((.state | test("^(metaDL|forcedMetaDL)$"))),
    first_idx(.progress == 1 and (.state | test("^(uploading|stalledUP|forcedUP|queuedUP|checkingUP)$"))),
    first_idx(.state | test("^(paused|stopped)(DL|UP)$")) ] as $c
  | ([$c[0] // $c[1]] + [$c[2], $c[3]]) | map(select(. != null)) | unique | .[]
' "$TMP/info" > "$TMP/picks"

while read -r idx; do
  hash=$(jq -r --argjson i "$idx" '.[$i].hash' "$TMP/info")
  echo "torrent index $idx: state $(jq -r --argjson i "$idx" '.[$i].state' "$TMP/info")"
  get "torrents/properties?hash=$hash" "  properties" && rec properties "$hash" "torrent_@N@_properties.json"
  get "torrents/trackers?hash=$hash" "  trackers" && rec trackers "$hash" "torrent_@N@_trackers.json"
  get "torrents/pieceStates?hash=$hash" "  pieceStates" && rec piecestates "$hash" "torrent_@N@_piecestates.json"
  get "torrents/files?hash=$hash" "  files" && rec files "$hash" "torrent_@N@_files.json"
  get "sync/torrentPeers?hash=$hash&rid=0" "  torrentPeers" && rec peers "$hash" "torrent_@N@_peers.json"
done < "$TMP/picks"

mkdir -p "$OUTDIR"
python3 "$HERE/anonymize.py" "$OUTDIR" < "$REC"
