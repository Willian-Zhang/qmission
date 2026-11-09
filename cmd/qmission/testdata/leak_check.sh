#!/bin/sh
# Check an anonymized capture dir for leftovers. Prints counts only, never values.
#   leak_check.sh <dir>   (exit 1 if anything is found or a JSON file is invalid)
set -eu
D=${1:?usage: leak_check.sh <dir>}
fail=0

bad_json=0
for f in "$D"/*.json; do
  jq empty "$f" 2>/dev/null || bad_json=$((bad_json + 1))
done
echo "files: $(ls "$D" | wc -l | tr -d ' '), invalid JSON: $bad_json"
[ "$bad_json" -eq 0 ] || fail=1

count() { # label, then a grep -o pipeline filter
  label=$1; shift
  n=$(cat "$D"/* | "$@" | wc -l | tr -d ' ')
  echo "$label: $n"
  [ "$n" -eq 0 ] || fail=1
}

# Fake hashes are zero-padded indexes: at least 30 leading zeros.
count "40-hex not fake" sh -c "grep -oiE '(^|[^0-9a-f])[0-9a-f]{40}([^0-9a-f]|$)' | grep -oiE '[0-9a-f]{40}' | grep -vE '^0{30}' || true"
count "64-hex not fake" sh -c "grep -oiE '[0-9a-f]{64}' | grep -vE '^0{50}' || true"
count "URLs not *.example" sh -c "grep -oiE '(https?|udp|wss?)://[^\" ,]+' | grep -viE '^[a-z]+://tracker[0-9]+\.example/announce' || true"
count "IPv4 outside 10.99" sh -c "grep -oE '(^|[^0-9.])([0-9]{1,3}\.){3}[0-9]{1,3}([^0-9.]|$)' | grep -oE '([0-9]{1,3}\.){3}[0-9]{1,3}' | grep -vE '^10\.99\.' || true"
count "IPv6-looking addresses" sh -c "grep -oiE '\"[0-9a-f]{1,4}(:[0-9a-f]{0,4}){3,7}' | grep -viE '^\"fd00::99' || true"
n=$(jq -r 'paths(scalars) as $p | select($p | last | tostring | test("pass|user|secret|_key$")) | getpath($p) | select(type == "string" and . != "")' "$D"/*.json | wc -l | tr -d ' ')
echo "non-empty password/user/key fields: $n"
[ "$n" -eq 0 ] || fail=1

[ "$fail" -eq 0 ] && echo "OK" || { echo "LEAK CHECK FAILED"; exit 1; }
