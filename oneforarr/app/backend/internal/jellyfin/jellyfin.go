// Package jellyfin tells Jellyfin to look for new files after an import, so
// they show up without waiting for its own scheduled scan.
package jellyfin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is a Jellyfin server and an API key (Jellyfin: Dashboard → API Keys).
type Client struct {
	base *url.URL
	key  string
	http *http.Client
}

// New returns a client, or an error for a bad address or a missing key.
func New(address, apiKey string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(address), "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("The Jellyfin address must look like http://localhost:8096.")
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("Enter a Jellyfin API key (Jellyfin: Dashboard → API Keys).")
	}
	return &Client{base: u, key: strings.TrimSpace(apiKey), http: &http.Client{Timeout: 15 * time.Second}}, nil
}

// Check signs in with the key and returns the server's name and version.
func (c *Client) Check(ctx context.Context) (string, error) {
	body, err := c.do(ctx, http.MethodGet, "/System/Info")
	if err != nil {
		return "", err
	}
	var info struct {
		ServerName string `json:"ServerName"`
		Version    string `json:"Version"`
	}
	if json.Unmarshal(body, &info) != nil || info.Version == "" {
		return "", errors.New("That address answered, but not like Jellyfin.")
	}
	return fmt.Sprintf("%s (Jellyfin %s)", info.ServerName, info.Version), nil
}

// Refresh asks Jellyfin to scan all its libraries for new and changed files.
func (c *Client) Refresh(ctx context.Context) error {
	_, err := c.do(ctx, http.MethodPost, "/Library/Refresh")
	return err
}

func (c *Client) do(ctx context.Context, method, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base.String()+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", `MediaBrowser Token="`+c.key+`"`)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Couldn't reach Jellyfin at %s. Is it running?", c.base.Host)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, errors.New("Jellyfin refused the API key.")
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return nil, fmt.Errorf("Jellyfin answered %d.", resp.StatusCode)
	}
	return body, nil
}
