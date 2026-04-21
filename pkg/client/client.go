package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New constructs a client targeting host:port.
// If httpClient is nil, a default client with a 10s timeout is used.
func New(host string, port int, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{
		baseURL:    fmt.Sprintf("http://%s:%d", host, port),
		httpClient: httpClient,
	}
}

// ErrNotFound is returned by GetTorrent when the server responds 404.
var ErrNotFound = fmt.Errorf("torrent not found")

// CheckHealth calls GET /readiness on the health port.
func (c *Client) CheckHealth(ctx context.Context, healthPort int) (*HealthCheck, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, err
	}
	u.Host = fmt.Sprintf("%s:%d", u.Hostname(), healthPort)
	u.Path = "/readiness"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var hc HealthCheck
	if err := json.NewDecoder(resp.Body).Decode(&hc); err != nil {
		return nil, fmt.Errorf("GET /readiness: decode response: %w", err)
	}
	return &hc, nil
}

// ListTorrents calls GET /api/torrents with optional filters and pagination.
func (c *Client) ListTorrents(ctx context.Context, p ListParams) ([]Torrent, error) {
	u, err := url.Parse(c.baseURL + "/api/torrents")
	if err != nil {
		return nil, err
	}

	q := u.Query()
	limit := p.Limit
	if limit <= 0 {
		limit = 50
	}
	q.Set("limit", strconv.Itoa(limit))
	q.Set("offset", strconv.Itoa(p.Offset))
	if p.State != "" {
		q.Set("state", p.State)
	}
	if p.ContentType != "" {
		q.Set("content_type", p.ContentType)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /api/torrents: HTTP %d", resp.StatusCode)
	}

	var torrents []Torrent
	if err := json.NewDecoder(resp.Body).Decode(&torrents); err != nil {
		return nil, fmt.Errorf("GET /api/torrents: decode response: %w", err)
	}
	return torrents, nil
}

// GetTorrent calls GET /api/torrents/{infohash}.
// Returns a non-nil error wrapping ErrNotFound if the server returns 404.
func (c *Client) GetTorrent(ctx context.Context, infohash string) (*Torrent, error) {
	path := "/api/torrents/" + infohash
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("GET %s: %w", path, ErrNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", path, resp.StatusCode)
	}

	var t Torrent
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return nil, fmt.Errorf("GET %s: decode response: %w", path, err)
	}
	return &t, nil
}

// UpdateTorrentState calls PATCH /api/torrents/{infohash} with {"state": state}.
// Returns an error if state is not in ValidStates or if the server rejects it.
func (c *Client) UpdateTorrentState(ctx context.Context, infohash, state string) error {
	valid := false
	for _, s := range ValidStates {
		if s == state {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("invalid state %q: must be one of %v", state, ValidStates)
	}

	path := "/api/torrents/" + infohash
	body, err := json.Marshal(map[string]string{"state": state})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("PATCH %s: HTTP %d", path, resp.StatusCode)
	}
	return nil
}

// DeleteTorrent calls DELETE /api/torrents/{infohash}.
func (c *Client) DeleteTorrent(ctx context.Context, infohash string) error {
	path := "/api/torrents/" + infohash
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+path, nil)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("DELETE %s: HTTP %d", path, resp.StatusCode)
	}
	return nil
}
