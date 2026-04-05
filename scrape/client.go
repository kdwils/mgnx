package scrape

//go:generate go run go.uber.org/mock/mockgen -destination=../mocks/mock_scraper.go -package=mocks github.com/kdwils/mgnx/scrape Scraper

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"net"
	"net/url"
	"time"
)

// Scraper performs a batch scrape against a single tracker.
type Scraper interface {
	Scrape(ctx context.Context, infohashes []string) ([]ScrapeResult, error)
}

const (
	connectMagic  int64 = 0x41727101980
	actionConnect int32 = 0
	actionScrape  int32 = 2
	maxPerBatch         = 74 // keeps request within a typical 1500-byte MTU
)

// ScrapeResult holds the per-torrent data returned by a tracker scrape.
type ScrapeResult struct {
	Infohash string
	Seeders  int32
	Leechers int32
	Complete int32 // all-time download count
}

// Client is a BEP-15 UDP tracker scrape client for a single tracker.
type Client struct {
	addr        string
	dialTimeout time.Duration
	readTimeout time.Duration
}

// NewClient parses trackerURL (must be udp://) and returns a Client.
func NewClient(trackerURL string, dialTimeout, readTimeout time.Duration) (*Client, error) {
	addr, err := parseUDPAddr(trackerURL)
	if err != nil {
		return nil, err
	}
	return &Client{addr: addr, dialTimeout: dialTimeout, readTimeout: readTimeout}, nil
}

// Scrape performs the BEP-15 connect + scrape sequence for all infohashes,
// splitting into batches of maxPerBatch automatically.
func (c *Client) Scrape(ctx context.Context, infohashes []string) ([]ScrapeResult, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", c.addr, err)
	}
	defer conn.Close()

	connID, err := c.connect(conn)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", c.addr, err)
	}

	var results []ScrapeResult
	for i := 0; i < len(infohashes); i += maxPerBatch {
		end := min(i+maxPerBatch, len(infohashes))
		batch := infohashes[i:end]

		batchResults, err := c.scrapeBatch(conn, connID, batch)
		if err != nil {
			return results, fmt.Errorf("scrape batch: %w", err)
		}
		results = append(results, batchResults...)
	}
	return results, nil
}

func (c *Client) dial(ctx context.Context) (net.Conn, error) {
	d := net.Dialer{Timeout: c.dialTimeout}
	return d.DialContext(ctx, "udp", c.addr)
}

func (c *Client) connect(conn net.Conn) (int64, error) {
	txID := rand.Int32()

	req := make([]byte, 16)
	binary.BigEndian.PutUint64(req[0:], uint64(connectMagic))
	binary.BigEndian.PutUint32(req[8:], uint32(actionConnect))
	binary.BigEndian.PutUint32(req[12:], uint32(txID))

	conn.SetDeadline(time.Now().Add(c.readTimeout)) //nolint:errcheck
	if _, err := conn.Write(req); err != nil {
		return 0, err
	}

	resp := make([]byte, 16)
	n, err := conn.Read(resp)
	if err != nil {
		return 0, err
	}
	if n < 16 {
		return 0, fmt.Errorf("connect response too short: %d bytes", n)
	}
	if int32(binary.BigEndian.Uint32(resp[0:])) != actionConnect {
		return 0, fmt.Errorf("unexpected action in connect response: %d", binary.BigEndian.Uint32(resp[0:]))
	}
	if int32(binary.BigEndian.Uint32(resp[4:])) != txID {
		return 0, fmt.Errorf("transaction ID mismatch in connect response")
	}

	return int64(binary.BigEndian.Uint64(resp[8:])), nil
}

func (c *Client) scrapeBatch(conn net.Conn, connID int64, infohashes []string) ([]ScrapeResult, error) {
	txID := rand.Int32()

	req := make([]byte, 16+20*len(infohashes))
	binary.BigEndian.PutUint64(req[0:], uint64(connID))
	binary.BigEndian.PutUint32(req[8:], uint32(actionScrape))
	binary.BigEndian.PutUint32(req[12:], uint32(txID))

	for i, ih := range infohashes {
		b, err := hex.DecodeString(ih)
		if err != nil {
			return nil, fmt.Errorf("invalid infohash %q: %w", ih, err)
		}
		copy(req[16+i*20:], b)
	}

	conn.SetDeadline(time.Now().Add(c.readTimeout)) //nolint:errcheck
	if _, err := conn.Write(req); err != nil {
		return nil, err
	}

	resp := make([]byte, 8+12*len(infohashes))
	n, err := conn.Read(resp)
	if err != nil {
		return nil, err
	}
	if n < 8 {
		return nil, fmt.Errorf("scrape response too short: %d bytes", n)
	}
	if int32(binary.BigEndian.Uint32(resp[0:])) != actionScrape {
		return nil, fmt.Errorf("unexpected action in scrape response: %d", binary.BigEndian.Uint32(resp[0:]))
	}
	if int32(binary.BigEndian.Uint32(resp[4:])) != txID {
		return nil, fmt.Errorf("transaction ID mismatch in scrape response")
	}

	count := (n - 8) / 12
	results := make([]ScrapeResult, count)
	for i := range count {
		offset := 8 + i*12
		results[i] = ScrapeResult{
			Infohash: infohashes[i],
			Seeders:  int32(binary.BigEndian.Uint32(resp[offset:])),
			Complete: int32(binary.BigEndian.Uint32(resp[offset+4:])),
			Leechers: int32(binary.BigEndian.Uint32(resp[offset+8:])),
		}
	}
	return results, nil
}

func parseUDPAddr(trackerURL string) (string, error) {
	u, err := url.Parse(trackerURL)
	if err != nil {
		return "", fmt.Errorf("parse tracker URL %q: %w", trackerURL, err)
	}
	if u.Scheme != "udp" {
		return "", fmt.Errorf("tracker %q is not a UDP tracker", trackerURL)
	}
	return u.Host, nil
}
