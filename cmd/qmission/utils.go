package main

import (
	"fmt"
	"io"
	"net/http"
	"time"
)

type JsonMap map[string]interface{}

// Check panics on an error. Only for tests and startup code; RPC handlers
// return errors instead.
func Check(e error) {
	if e != nil {
		panic(e)
	}
}

func Any(vs []string, dst string) bool {
	for _, v := range vs {
		if v == dst {
			return true
		}
	}
	return false
}

// DoGetWithCookies downloads a .torrent file for torrent-add.
func DoGetWithCookies(path string, cookies *string) ([]byte, error) {
	httpReq, err := http.NewRequest("GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("torrent-add: invalid URL: %w", err)
	}
	if cookies != nil {
		httpReq.Header.Add("Cookie", *cookies)
	}
	cl := &http.Client{Timeout: 60 * time.Second}
	resp, err := cl.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("torrent-add: downloading the torrent: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("torrent-add: downloading the torrent: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxRequestBody))
	if err != nil {
		return nil, fmt.Errorf("torrent-add: downloading the torrent: %w", err)
	}
	return data, nil
}
