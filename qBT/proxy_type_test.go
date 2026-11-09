package qBT

import (
	"encoding/json"
	"testing"
)

// Ported from Xmister/Reflection 5dc9aae, values corrected to qBT's enum (SOCKS4 = 5).
func TestProxyTypeUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected ProxyType
	}{
		// qBT >= 4.6: Utils::String::fromEnum(proxyConf.type)
		{"string None", `{"proxy_type":"None"}`, ProxyTypeNone},
		{"string HTTP", `{"proxy_type":"HTTP"}`, ProxyTypeHTTP},
		{"string SOCKS5", `{"proxy_type":"SOCKS5"}`, ProxyTypeSOCKS5},
		{"string SOCKS4", `{"proxy_type":"SOCKS4"}`, ProxyTypeSOCKS4},
		{"string unknown", `{"proxy_type":"I2P"}`, ProxyTypeUnknown},
		// qBT < 4.6: int
		{"int -1 (disabled)", `{"proxy_type":-1}`, ProxyTypeNone},
		{"int 0", `{"proxy_type":0}`, ProxyTypeNone},
		{"int 1", `{"proxy_type":1}`, ProxyTypeHTTP},
		{"int 2", `{"proxy_type":2}`, ProxyTypeSOCKS5},
		{"int 3 (HTTP_PW)", `{"proxy_type":3}`, ProxyTypeHTTP},
		{"int 4 (SOCKS5_PW)", `{"proxy_type":4}`, ProxyTypeSOCKS5},
		{"int 5", `{"proxy_type":5}`, ProxyTypeSOCKS4},
		{"int unknown", `{"proxy_type":42}`, ProxyTypeUnknown},
		{"numeric string", `{"proxy_type":"2"}`, ProxyTypeSOCKS5},
		{"null", `{"proxy_type":null}`, ProxyTypeNone},
		{"absent", `{}`, ProxyTypeNone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var pref Preferences
			if err := json.Unmarshal([]byte(tt.input), &pref); err != nil {
				t.Fatalf("unexpected error for %s: %v", tt.input, err)
			}
			if pref.Proxy_type != tt.expected {
				t.Errorf("%s: expected %v, got %v", tt.input, tt.expected, pref.Proxy_type)
			}
		})
	}
}

func TestProxyTypeInvalidJSON(t *testing.T) {
	var pref Preferences
	if err := json.Unmarshal([]byte(`{"proxy_type":{}}`), &pref); err == nil {
		t.Error("expected an error for an object value")
	}
}

// The case from h31/Reflection issue #8: session-get panicked on qBT >= 4.6.
func TestPreferencesWithStringProxyType(t *testing.T) {
	payload := `{"add_stopped_enabled":true,"proxy_type":"None","proxy_ip":"","proxy_port":8080,"web_ui_port":8088}`
	var pref Preferences
	if err := json.Unmarshal([]byte(payload), &pref); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if pref.Proxy_type != ProxyTypeNone || pref.Proxy_port != 8080 || pref.Web_ui_port != 8088 {
		t.Errorf("unexpected values: %+v", pref)
	}
	if !pref.AddStopped() {
		t.Error("add_stopped_enabled not decoded")
	}
	var old Preferences
	if err := json.Unmarshal([]byte(`{"start_paused_enabled":true,"proxy_type":2}`), &old); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if !old.AddStopped() || old.Proxy_type != ProxyTypeSOCKS5 {
		t.Errorf("unexpected values: %+v", old)
	}
}

func TestProxyTypeString(t *testing.T) {
	tests := []struct {
		proxyType ProxyType
		expected  string
	}{
		{ProxyTypeNone, "None"},
		{ProxyTypeHTTP, "HTTP"},
		{ProxyTypeSOCKS5, "SOCKS5"},
		{ProxyTypeSOCKS4, "SOCKS4"},
		{ProxyType(99), "Unknown(99)"},
	}
	for _, tt := range tests {
		if got := tt.proxyType.String(); got != tt.expected {
			t.Errorf("expected %s, got %s", tt.expected, got)
		}
	}
}
