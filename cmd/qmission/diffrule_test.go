package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestChangedKeys(t *testing.T) {
	req := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(`{
		"a": 1, "b": 2.0, "c": "x", "d": [1, 2], "e": true, "junk": 5, "f": 3, "g": {"k": 1}
	}`), &req); err != nil {
		t.Fatal(err)
	}
	type sub struct {
		K int `json:"k"`
	}
	currents := []JsonMap{
		{"a": int64(1), "b": 3, "c": "x", "d": []int{1, 2}, "e": false, "g": sub{K: 1}},
		{"b": json.Number("2"), "e": false},
	}
	isProto := func(k string) bool { return k != "junk" }
	got := changedKeys(req, currents, isProto)
	want := []string{"e", "f"} // b matches the second torrent; f is missing everywhere
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changedKeys = %v, want %v", got, want)
	}
}

func TestUnmappableError(t *testing.T) {
	err := unmappableError([]string{"peer-limit", "blocklist-url"})
	if err.Error() != "qBittorrent can't store these fields, nothing was changed: blocklist-url, peer-limit" {
		t.Fatal(err)
	}
}
