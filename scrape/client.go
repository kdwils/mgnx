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

// Client is a BEP-15 UDP tracker scrape client that queries one or more trackers
// in order, stopping at the first tracker that returns any seeders.
type Client struct {
	addrs       []string
	dialTimeout time.Duration
	readTimeout time.Duration
}

// NewClient parses one or more UDP tracker URLs and returns a Client.
// URLs that fail to parse are silently skipped; an error is returned only if
// no valid URLs remain.
func NewClient(dialTimeout, readTimeout time.Duration, trackerURLs ...string) (*Client, error) {
	addrs := make([]string, 0, len(trackerURLs))
	for _, u := range trackerURLs {
		addr, err := parseUDPAddr(u)
		if err != nil {
			continue
		}
		addrs = append(addrs, addr)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("no valid UDP tracker URLs provided")
	}
	return &Client{addrs: addrs, dialTimeout: dialTimeout, readTimeout: readTimeout}, nil
}

func (c *Client) shuffledAddrs() []string {
	addrs := make([]string, len(c.addrs))
	copy(addrs, c.addrs)
	rand.Shuffle(len(addrs), func(i, j int) { addrs[i], addrs[j] = addrs[j], addrs[i] })
	return addrs
}

// Scrape queries trackers in a random order and returns results from the first
// tracker that reports any seeders.
func (c *Client) Scrape(ctx context.Context, infohashes []string) ([]ScrapeResult, error) {
	for _, addr := range c.shuffledAddrs() {
		if ctx.Err() != nil {
			break
		}
		results, err := c.scrapeTracker(ctx, addr, infohashes)
		if err != nil {
			continue
		}
		for _, r := range results {
			if r.Seeders > 0 {
				return results, nil
			}
		}
	}
	return nil, nil
}

// scrapeTracker performs the BEP-15 connect + scrape sequence against a single
// tracker address, splitting infohashes into batches of maxPerBatch.
func (c *Client) scrapeTracker(ctx context.Context, addr string, infohashes []string) ([]ScrapeResult, error) {
	conn, err := c.dialAddr(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	defer conn.Close()

	connID, err := c.connect(conn)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", addr, err)
	}

	var results []ScrapeResult
	for i := 0; i < len(infohashes); i += maxPerBatch {
		end := min(i+maxPerBatch, len(infohashes))
		batchResults, err := c.scrapeBatch(conn, connID, infohashes[i:end])
		if err != nil {
			return results, fmt.Errorf("scrape batch: %w", err)
		}
		results = append(results, batchResults...)
	}
	return results, nil
}

func (c *Client) dialAddr(ctx context.Context, addr string) (net.Conn, error) {
	d := net.Dialer{Timeout: c.dialTimeout}
	return d.DialContext(ctx, "udp", addr)
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
