// Package downloads talks to the download client, qBittorrent.
//
// qBittorrent runs inside the gluetun VPN container, so its address is
// gluetun's (http://gluetun:8080 on the Docker network). This app never
// downloads anything itself.
package downloads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

// QBittorrent is a client for qBittorrent's Web API (v2).
type QBittorrent struct {
	base     *url.URL
	username string
	password string
	http     *http.Client
}

// NewQBittorrent returns a client for the qBittorrent at address.
func NewQBittorrent(address, username, password string) (*QBittorrent, error) {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(address), "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, errors.New("The qBittorrent address must look like http://gluetun:8080.")
	}
	jar, _ := cookiejar.New(nil) // holds qBittorrent's sign-in cookie (SID)
	return &QBittorrent{
		base:     base,
		username: username,
		password: password,
		http:     &http.Client{Jar: jar, Timeout: 15 * time.Second},
	}, nil
}

// Login signs in to qBittorrent.
func (q *QBittorrent) Login(ctx context.Context) error {
	form := url.Values{"username": {q.username}, "password": {q.password}}
	body, status, err := q.do(ctx, http.MethodPost, "/api/v2/auth/login", form)
	if err != nil {
		return err
	}
	// qBittorrent 5.2 and later answer 204 (no text) when the sign-in
	// works; older versions answer 200 with "Ok.".
	switch {
	case status == http.StatusNoContent:
		return nil
	case status == http.StatusForbidden:
		return errors.New("qBittorrent has blocked this address after too many failed sign-ins. Wait, or restart qBittorrent.")
	case status == http.StatusUnauthorized:
		return errors.New("qBittorrent refused the username or password.")
	case status != http.StatusOK:
		return fmt.Errorf("qBittorrent answered %d when signing in.", status)
	case strings.TrimSpace(body) != "Ok.":
		return errors.New("qBittorrent refused the username or password.")
	}
	return nil
}

// Version signs in and returns qBittorrent's version, e.g. "v5.1.2". It's
// what the Test button on the Settings page uses.
func (q *QBittorrent) Version(ctx context.Context) (string, error) {
	if err := q.Login(ctx); err != nil {
		return "", err
	}
	body, status, err := q.do(ctx, http.MethodGet, "/api/v2/app/version", nil)
	if err != nil {
		return "", err
	}
	if !success(status) {
		return "", fmt.Errorf("qBittorrent answered %d when asked for its version.", status)
	}
	return strings.TrimSpace(body), nil
}

// EnsureCategory makes sure the category exists, saving into savePath (a
// path as qBittorrent's container sees it). An existing category is left
// as it is.
func (q *QBittorrent) EnsureCategory(ctx context.Context, name, savePath string) error {
	form := url.Values{"category": {name}, "savePath": {savePath}}
	_, status, err := q.do(ctx, http.MethodPost, "/api/v2/torrents/createCategory", form)
	if err != nil {
		return err
	}
	// 409 means it already exists.
	if !success(status) && status != http.StatusConflict {
		return fmt.Errorf("qBittorrent answered %d when creating the %q category.", status, name)
	}
	return nil
}

// Add gives qBittorrent a magnet link or a .torrent address, in category,
// with an optional tag to find it again. qBittorrent fetches .torrent files
// itself, through the VPN.
func (q *QBittorrent) Add(ctx context.Context, link, category, tag string) error {
	form := url.Values{"urls": {link}, "category": {category}}
	if tag != "" {
		form.Set("tags", tag)
	}
	body, status, err := q.do(ctx, http.MethodPost, "/api/v2/torrents/add", form)
	if err != nil {
		return err
	}
	if !success(status) || strings.TrimSpace(body) == "Fails." {
		return fmt.Errorf("qBittorrent didn't accept the torrent (answer %d %s).", status, strings.TrimSpace(body))
	}
	return nil
}

// Torrent is one torrent as qBittorrent reports it.
type Torrent struct {
	Hash        string  `json:"hash"`
	Name        string  `json:"name"`
	Progress    float64 `json:"progress"` // 0..1
	State       string  `json:"state"`
	ContentPath string  `json:"content_path"` // the file, or the folder of a multi-file torrent
	SavePath    string  `json:"save_path"`
	Size        int64   `json:"size"`
	ETA         int64   `json:"eta"` // seconds; 8640000 means unknown
	Tags        string  `json:"tags"`
	NumSeeds    int     `json:"num_seeds"`  // seeders connected now
	DLSpeed     int64   `json:"dlspeed"`    // bytes per second
	Downloaded  int64   `json:"downloaded"` // bytes so far
}

// Done reports whether the torrent has finished downloading and its files
// are in place (not still being checked or moved).
func (t Torrent) Done() bool {
	if t.Progress < 1 {
		return false
	}
	switch t.State {
	case "checkingUP", "checkingDL", "checkingResumeData", "moving", "missingFiles", "error", "allocating":
		return false
	}
	return true
}

// Torrents lists the torrents in category (all categories when empty).
func (q *QBittorrent) Torrents(ctx context.Context, category string) ([]Torrent, error) {
	path := "/api/v2/torrents/info"
	if category != "" {
		path += "?" + url.Values{"category": {category}}.Encode()
	}
	body, status, err := q.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if !success(status) {
		return nil, fmt.Errorf("qBittorrent answered %d when asked for its torrents.", status)
	}
	var list []Torrent
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		return nil, errors.New("qBittorrent's torrent list couldn't be read.")
	}
	return list, nil
}

// HasTag reports whether the torrent carries tag.
func (t Torrent) HasTag(tag string) bool {
	for _, x := range strings.Split(t.Tags, ",") {
		if strings.TrimSpace(x) == tag {
			return true
		}
	}
	return false
}

// success is any 2xx answer; newer qBittorrent versions use 204 for some.
func success(status int) bool { return status >= 200 && status < 300 }

// do sends one request. form, when not nil, is sent as a form body.
// do sends one request. When qBittorrent answers 403 because the sign-in
// has expired (or qBittorrent restarted), it signs in again and retries once.
func (q *QBittorrent) do(ctx context.Context, method, path string, form url.Values) (string, int, error) {
	body, status, err := q.send(ctx, method, path, form)
	if err == nil && status == http.StatusForbidden && path != "/api/v2/auth/login" {
		if q.Login(ctx) == nil {
			return q.send(ctx, method, path, form)
		}
	}
	return body, status, err
}

func (q *QBittorrent) send(ctx context.Context, method, path string, form url.Values) (string, int, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, q.base.String()+path, body)
	if err != nil {
		return "", 0, err
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	// qBittorrent checks that Referer matches its own address.
	req.Header.Set("Referer", q.base.String())
	resp, err := q.http.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("Couldn't reach qBittorrent at %s. Is the VPN container running?", q.base.Host)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return "", 0, fmt.Errorf("reading qBittorrent's answer: %w", err)
	}
	return string(raw), resp.StatusCode, nil
}

// Delete removes a torrent and its downloaded files.
func (q *QBittorrent) Delete(ctx context.Context, hash string) error {
	form := url.Values{"hashes": {hash}, "deleteFiles": {"true"}}
	_, status, err := q.do(ctx, http.MethodPost, "/api/v2/torrents/delete", form)
	if err != nil {
		return err
	}
	if !success(status) {
		return fmt.Errorf("qBittorrent answered %d when asked to remove a torrent.", status)
	}
	return nil
}
