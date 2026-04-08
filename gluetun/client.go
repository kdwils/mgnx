package gluetun

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
)

// HTTPDoer is satisfied by *http.Client and any test double.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// PublicIPResponse is the response from the Gluetun /v1/publicip/ip endpoint.
type PublicIPResponse struct {
	PublicIP     string `json:"public_ip"`
	Region       string `json:"region"`
	Country      string `json:"country"`
	City         string `json:"city"`
	Location     string `json:"location"`
	Organization string `json:"organization"`
	PostalCode   string `json:"postal_code"`
	Timezone     string `json:"timezone"`
}

// IP parses the PublicIP field as a net.IP.
func (r *PublicIPResponse) IP() net.IP {
	return net.ParseIP(r.PublicIP)
}

// Client fetches public IP information from a Gluetun instance.
type Client struct {
	endpoint string
	http     HTTPDoer
}

// New returns a Client that calls endpoint using the provided HTTPDoer.
func New(endpoint string, http HTTPDoer) *Client {
	return &Client{endpoint: endpoint, http: http}
}

// FetchPublicIP calls the Gluetun public IP endpoint and returns the parsed response.
func (c *Client) FetchPublicIP(ctx context.Context) (*PublicIPResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("gluetun: build request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gluetun: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gluetun: unexpected status %d", resp.StatusCode)
	}

	var result PublicIPResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("gluetun: decode response: %w", err)
	}

	return &result, nil
}
