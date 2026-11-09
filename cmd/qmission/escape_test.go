package main

import (
	"encoding/json"
	"testing"
)

func TestEscapedStringIsValidJSON(t *testing.T) {
	cases := map[string]string{
		"ascii":         `plain "quoted" \ back/slash`,
		"control":       "tab\there\x01\x7f",
		"cjk":           "中文 日本語 한국어",
		"emoji":         "🎬 film 😀",
		"supplementary": "𠀀𝄞",
		"combining":     "e\u0301",
		"html":          "<a&b>",
		"line seps":     "a\u2028b\u2029c",
	}
	for name, in := range cases {
		out, err := json.Marshal(map[string]escapedString{"v": EscapeString(in)})
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		for _, c := range out {
			if c >= 0x80 {
				t.Fatalf("%s: non-ASCII byte in output %q", name, out)
			}
		}
		var back map[string]string
		if err := json.Unmarshal(out, &back); err != nil {
			t.Fatalf("%s: output isn't valid JSON: %v (%q)", name, err, out)
		}
		if back["v"] != in {
			t.Fatalf("%s: round trip %q != %q", name, back["v"], in)
		}
	}

	// Invalid UTF-8 becomes U+FFFD instead of breaking the response.
	out, err := json.Marshal(EscapeString("bad\xffbyte"))
	if err != nil {
		t.Fatal(err)
	}
	var back string
	if err := json.Unmarshal(out, &back); err != nil || back != "bad\uFFFDbyte" {
		t.Fatalf("invalid UTF-8: %q, %v", back, err)
	}
}
