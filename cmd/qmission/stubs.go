package main

import (
	"encoding/json"
	"errors"
	"fmt"

	log "github.com/sirupsen/logrus"
)

// Methods qBittorrent has no counterpart for, answered with harmless values so
// TrguiNG's buttons don't fail (rpc-spec.md 4.1.4 §4.3, §4.4, §4.8).

func init() {
	registerRPC("group-get", GroupGet)
	registerRPC("group-set", GroupSet)
	registerRPC("port-test", PortTest)
	registerRPC("blocklist-update", BlocklistUpdate)
}

// GroupGet: qBT has no bandwidth groups, so the list is always empty.
func GroupGet(args json.RawMessage) (JsonMap, error) {
	return JsonMap{"group": []interface{}{}}, nil
}

// GroupSet accepts and drops the group (TrguiNG never calls it; see
// findings/trguing-source.md).
func GroupSet(args json.RawMessage) (JsonMap, error) {
	log.Debug("group-set: qBittorrent has no bandwidth groups, ignored")
	return JsonMap{}, nil
}

// PortTest approximates Transmission's external port check with qBT's
// connection_status: "connected" means qBT has seen incoming connections;
// "firewalled" and "disconnected" count as closed. Like Transmission, an asked
// ip_protocol is echoed; without one it stays unset (qBT doesn't say which).
func PortTest(args json.RawMessage) (JsonMap, error) {
	var req struct {
		IPProtocol *string `json:"ip_protocol"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, fmt.Errorf("port-test: invalid arguments: %w", err)
	}
	if req.IPProtocol != nil && *req.IPProtocol != "ipv4" && *req.IPProtocol != "ipv6" {
		return nil, errors.New("invalid ip protocol string")
	}
	state, err := serverState()
	if err != nil {
		return nil, err
	}
	resp := JsonMap{"port-is-open": state.Connection_status == "connected"}
	if req.IPProtocol != nil {
		resp["ip_protocol"] = *req.IPProtocol
	}
	return resp, nil
}

// BlocklistUpdate: qBT loads its IP filter from a local file and has no URL to
// fetch, so there is nothing to update; session-get reports blocklist-size 0 too.
func BlocklistUpdate(args json.RawMessage) (JsonMap, error) {
	log.Debug("blocklist-update: qBittorrent has no blocklist URL, nothing to fetch")
	return JsonMap{"blocklist-size": 0}, nil
}
