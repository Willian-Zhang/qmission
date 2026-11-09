package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
)

func TestLogRPCRequest(t *testing.T) {
	var buf bytes.Buffer
	prevOut, prevLevel := log.StandardLogger().Out, log.GetLevel()
	log.SetOutput(&buf)
	log.SetLevel(log.InfoLevel)
	t.Cleanup(func() { log.SetOutput(prevOut); log.SetLevel(prevLevel) })

	logRPCRequest("torrent-get", JsonMap{"torrents": []JsonMap{{"name": "secret name"}, {}}}, nil, 12*time.Millisecond)
	logRPCRequest("torrent-add", nil, errors.New("boom"), time.Millisecond)

	out := buf.String()
	for _, want := range []string{"method=torrent-get", "ok=true", "ms=12", "torrents=2", "method=torrent-add", "ok=false"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "secret") {
		t.Errorf("log leaks torrent data:\n%s", out)
	}
}
