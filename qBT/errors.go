package qBT

import (
	"errors"
	"fmt"
)

var (
	// ErrInvalidCredentials: qBT rejected the username/password (4.x body "Fails.",
	// WebAPI >= 2.14 status 401; 401 can also mean Host-header/CSRF validation failed).
	// Login is not retried until SetCredentials changes the credentials, so a bad
	// password can't run into qBT's per-IP ban.
	ErrInvalidCredentials = errors.New("qBittorrent rejected the credentials")
	// ErrBanned: qBT answered 403 on auth/login, the bridge's IP is banned
	// after too many failed logins (WebUI/BanDuration, default 1 h).
	ErrBanned = errors.New("qBittorrent banned this IP after too many failed logins")
	// ErrUnsupportedVersion: qBT older than 4.1 (no /api/v2 or WebAPI < 2.0).
	ErrUnsupportedVersion = errors.New("unsupported qBittorrent version (qmission needs qBittorrent 4.1+, WebAPI 2.0+)")
	// ErrForbidden: an API call still got 403 after a fresh login.
	ErrForbidden = errors.New("qBittorrent returned 403 Forbidden after re-login")
	// ErrTorrentNotFound: the hash is not in the torrent table.
	ErrTorrentNotFound = errors.New("torrent not found")
	// ErrAddRejected: torrents/add added nothing (WebAPI < 2.14 body "Fails.",
	// >= 2.14 status 409), e.g. a duplicate or an invalid magnet link.
	ErrAddRejected = errors.New("qBittorrent did not add the torrent")
)

// HTTPError is a non-2xx answer from qBT (other than the 403 handled by re-login).
type HTTPError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string // truncated
}

func (e *HTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("qBittorrent %s %s: HTTP %d", e.Method, e.Path, e.StatusCode)
	}
	return fmt.Sprintf("qBittorrent %s %s: HTTP %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

func newHTTPError(method, path string, status int, body []byte) *HTTPError {
	const maxBody = 200
	b := string(body)
	if len(b) > maxBody {
		b = b[:maxBody] + "..."
	}
	return &HTTPError{Method: method, Path: path, StatusCode: status, Body: b}
}

// decodeError wraps a JSON decoding failure without including the payload,
// which can contain torrent data.
func decodeError(path string, data []byte, err error) error {
	return fmt.Errorf("qBittorrent %s: decoding response (%d bytes): %w", path, len(data), err)
}
