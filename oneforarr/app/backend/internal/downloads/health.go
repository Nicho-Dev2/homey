package downloads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// Transfer is qBittorrent's connection summary (Transfer panel / status bar).
type Transfer struct {
	DHTNodes int `json:"dht_nodes"`
	// Status is "connected", "firewalled" (works, but can't accept incoming
	// peers; normal behind a VPN) or "disconnected".
	Status  string `json:"connection_status"`
	DLSpeed int64  `json:"dl_info_speed"` // bytes per second
}

// Transfer returns the connection summary.
func (q *QBittorrent) Transfer(ctx context.Context) (Transfer, error) {
	body, status, err := q.do(ctx, http.MethodGet, "/api/v2/transfer/info", nil)
	if err != nil {
		return Transfer{}, err
	}
	if !success(status) {
		return Transfer{}, fmt.Errorf("qBittorrent answered %d when asked for its connection.", status)
	}
	var t Transfer
	if json.Unmarshal([]byte(body), &t) != nil {
		return Transfer{}, errors.New("qBittorrent's connection info couldn't be read.")
	}
	return t, nil
}

// DHTEnabled says whether DHT is on in qBittorrent's settings. With DHT off,
// "0 DHT nodes" is normal and says nothing about the connection.
func (q *QBittorrent) DHTEnabled(ctx context.Context) (bool, error) {
	body, status, err := q.do(ctx, http.MethodGet, "/api/v2/app/preferences", nil)
	if err != nil {
		return false, err
	}
	if !success(status) {
		return false, fmt.Errorf("qBittorrent answered %d when asked for its settings.", status)
	}
	var p struct {
		DHT bool `json:"dht"`
	}
	if json.Unmarshal([]byte(body), &p) != nil {
		return false, errors.New("qBittorrent's settings couldn't be read.")
	}
	return p.DHT, nil
}

// Shutdown quits qBittorrent. In the linuxserver image the program, or the
// whole container (restart: unless-stopped), starts again by itself within
// seconds, with fresh connections.
func (q *QBittorrent) Shutdown(ctx context.Context) error {
	_, status, err := q.do(ctx, http.MethodPost, "/api/v2/app/shutdown", nil)
	if err != nil {
		return err
	}
	if !success(status) {
		return fmt.Errorf("qBittorrent answered %d when asked to restart.", status)
	}
	return nil
}
