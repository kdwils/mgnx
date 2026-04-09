//go:build functional

package functional

import (
	"context"
	"encoding/hex"
	"encoding/xml"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/kdwils/mgnx/config"
	"github.com/kdwils/mgnx/db"
	"github.com/kdwils/mgnx/db/gen"
	"github.com/kdwils/mgnx/dht"
	"github.com/kdwils/mgnx/indexer"
	"github.com/kdwils/mgnx/logger"
	"github.com/kdwils/mgnx/metadata"
	"github.com/kdwils/mgnx/mocks"
	"github.com/kdwils/mgnx/scrape"
	"github.com/kdwils/mgnx/service"
	"github.com/kdwils/mgnx/torznab"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"go.uber.org/mock/gomock"
)

var (
	movieHash = [20]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x14}
	tvHash    = [20]byte{0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28, 0x29, 0x2a, 0x2b, 0x2c, 0x2d, 0x2e, 0x2f, 0x30, 0x31, 0x32, 0x33, 0x34}
	rejHash   = [20]byte{0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48, 0x49, 0x4a, 0x4b, 0x4c, 0x4d, 0x4e, 0x4f, 0x50, 0x51, 0x52, 0x53, 0x54}
)

func TestEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	pgContainer, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("mgnx"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pgContainer.Terminate(ctx) })

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	pool, err := db.Connect(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(cancel)

	require.NoError(t, db.RunMigrations(pool))

	queries := gen.New(pool)

	ctrl := gomock.NewController(t)
	discoveredCh := make(chan dht.DiscoveredPeers, 50)

	crawler := mocks.NewMockCrawler(ctrl)
	crawler.EXPECT().Start(gomock.Any()).Return(nil)
	crawler.EXPECT().Stop(gomock.Any())
	crawler.EXPECT().Infohashes().Return((<-chan dht.DiscoveredPeers)(discoveredCh)).AnyTimes()

	fetcher := mocks.NewMockFetcher(ctrl)
	scraper := mocks.NewMockScraper(ctrl)

	movieHex := hex.EncodeToString(movieHash[:])
	tvHex := hex.EncodeToString(tvHash[:])
	rejHex := hex.EncodeToString(rejHash[:])

	fetcher.EXPECT().Fetch(gomock.Any(), movieHash, gomock.Any()).Return(&metadata.TorrentInfo{
		Name:      "The.Matrix.1999.1080p.BluRay.x264-FGT",
		TotalSize: 8 * 1024 * 1024 * 1024,
		Files:     []metadata.FileInfo{{Path: "The.Matrix.1999.1080p.BluRay.x264.mkv", Size: 8 * 1024 * 1024 * 1024}},
	}, nil)

	fetcher.EXPECT().Fetch(gomock.Any(), tvHash, gomock.Any()).Return(&metadata.TorrentInfo{
		Name:      "Breaking.Bad.S01E01.720p.BluRay.x264-GROUP",
		TotalSize: 4 * 1024 * 1024 * 1024,
		Files:     []metadata.FileInfo{{Path: "Breaking.Bad.S01E01.720p.BluRay.x264.mkv", Size: 4 * 1024 * 1024 * 1024}},
	}, nil)

	fetcher.EXPECT().Fetch(gomock.Any(), rejHash, gomock.Any()).Return(&metadata.TorrentInfo{
		Name:      "some.malware.cracker",
		TotalSize: 200 * 1024 * 1024,
		Files:     []metadata.FileInfo{{Path: "cracker.exe", Size: 200 * 1024 * 1024}},
	}, nil)

	scraper.EXPECT().Scrape(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, hashes []string) ([]scrape.ScrapeResult, error) {
		results := make([]scrape.ScrapeResult, len(hashes))
		for i, h := range hashes {
			switch h {
			case movieHex:
				results[i] = scrape.ScrapeResult{Infohash: h, Seeders: 50, Leechers: 5}
			case tvHex:
				results[i] = scrape.ScrapeResult{Infohash: h, Seeders: 25, Leechers: 2}
			default:
				results[i] = scrape.ScrapeResult{Infohash: h}
			}
		}
		return results, nil
	}).AnyTimes()

	cfg := config.Config{
		Indexer: config.Indexer{
			Workers:            4,
			MaxConcurrentPeers: 5,
			RequestTimeout:     6 * time.Second,
			MaxPeers:           1,
			RateLimit:          2.0,
			RateBurst:          4,
			MinSize:            50 * 1024 * 1024,
			MaxSize:            150 * 1024 * 1024 * 1024,
			AllowedExtensions:  []string{".mkv", ".mp4", ".avi", ".mov", ".wmv", ".m4v", ".ts", ".m2ts", ".vob", ".flv", ".webm", ".srt", ".ass", ".ssa"},
		},
		Scrape: config.Scrape{
			PollInterval: 200 * time.Millisecond,
			BatchSize:    74,
			Trackers:     []string{"udp://192.0.2.1:6969/announce"},
		},
		Server: config.Server{LogLevel: "error"},
	}

	idxWorker := indexer.New(crawler, fetcher, queries, cfg.Indexer)
	require.NoError(t, crawler.Start(ctx))
	t.Cleanup(func() { crawler.Stop(ctx) })
	go idxWorker.Run(ctx)

	scrapeWorker := scrape.New(queries, scraper, cfg.Scrape)
	go scrapeWorker.Run(ctx)

	time.Sleep(100 * time.Millisecond)

	discoveredCh <- dht.DiscoveredPeers{
		Infohash: movieHash,
		Peers:    []dht.PeerAddr{{SourceIP: net.ParseIP("127.0.0.1"), Port: 6881}},
	}
	discoveredCh <- dht.DiscoveredPeers{
		Infohash: tvHash,
		Peers:    []dht.PeerAddr{{SourceIP: net.ParseIP("127.0.0.1"), Port: 6881}},
	}
	discoveredCh <- dht.DiscoveredPeers{
		Infohash: rejHash,
		Peers:    []dht.PeerAddr{{SourceIP: net.ParseIP("127.0.0.1"), Port: 6881}},
	}

	ln, err := net.Listen("tcp", ":0")
	require.NoError(t, err)

	svc := service.New(queries, cfg)
	srv := torznab.New(0, logger.New("error"), svc)
	go srv.ServeListener(ctx, ln)

	serverURL := "http://127.0.0.1:" + strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)

	if !t.Run("IndexMovie", func(t *testing.T) {
		waitForState(t, ctx, queries, movieHex, gen.TorrentStateActive, 30*time.Second)
	}) {
		t.FailNow()
	}

	if !t.Run("IndexTV", func(t *testing.T) {
		waitForState(t, ctx, queries, tvHex, gen.TorrentStateActive, 30*time.Second)
	}) {
		t.FailNow()
	}

	if !t.Run("IndexRejected", func(t *testing.T) {
		waitForState(t, ctx, queries, rejHex, gen.TorrentStateRejected, 15*time.Second)
	}) {
		t.FailNow()
	}

	t.Run("Capabilities", func(t *testing.T) {
		resp, err := http.Get(serverURL + "/api?t=caps")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		var caps service.XMLCaps
		require.NoError(t, xml.NewDecoder(resp.Body).Decode(&caps))
		assert.Equal(t, "mgnx", caps.Server.Title)
	})

	t.Run("SearchAll", func(t *testing.T) {
		rss := getRSS(t, serverURL+"/api?t=search")
		assertGUID(t, rss, movieHex)
		assertGUID(t, rss, tvHex)
	})

	t.Run("RejectedNotVisible", func(t *testing.T) {
		rss := getRSS(t, serverURL+"/api?t=search")
		assertNoGUID(t, rss, rejHex)
	})

	t.Run("Pagination", func(t *testing.T) {
		rss1 := getRSS(t, serverURL+"/api?t=search&limit=1&offset=0")
		require.Len(t, rss1.Channel.Items, 1)
		rss2 := getRSS(t, serverURL+"/api?t=search&limit=1&offset=1")
		require.Len(t, rss2.Channel.Items, 1)
		assert.NotEqual(t, rss1.Channel.Items[0].GUID.Value, rss2.Channel.Items[0].GUID.Value)
	})

	t.Run("UnknownFunction", func(t *testing.T) {
		resp, err := http.Get(serverURL + "/api?t=unknown")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})
}

func waitForState(t *testing.T, ctx context.Context, queries gen.Querier, infohashHex string, wantState gen.TorrentState, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		torrent, err := queries.GetTorrentByInfohash(ctx, infohashHex)
		if err == nil && torrent.State == wantState {
			return
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			t.Fatalf("context cancelled waiting for %s to reach state %s", infohashHex, wantState)
		}
	}
	torrent, err := queries.GetTorrentByInfohash(ctx, infohashHex)
	if err != nil {
		t.Fatalf("torrent %s not found after %s: %v", infohashHex, timeout, err)
	}
	t.Fatalf("torrent %s stuck in state %s, want %s after %s", infohashHex, torrent.State, wantState, timeout)
}

func getRSS(t *testing.T, url string) service.XMLRSS {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var rss service.XMLRSS
	require.NoError(t, xml.NewDecoder(resp.Body).Decode(&rss))
	return rss
}

func assertGUID(t *testing.T, rss service.XMLRSS, guid string) {
	t.Helper()
	for _, item := range rss.Channel.Items {
		if item.GUID.Value == guid {
			return
		}
	}
	t.Errorf("guid %s not found in feed (got %d items)", guid, len(rss.Channel.Items))
}

func assertNoGUID(t *testing.T, rss service.XMLRSS, guid string) {
	t.Helper()
	for _, item := range rss.Channel.Items {
		if item.GUID.Value == guid {
			t.Errorf("guid %s unexpectedly found in feed", guid)
			return
		}
	}
}

func attrValue(item service.XMLItem, name string) string {
	for _, a := range item.Attrs {
		if a.Name == name {
			return a.Value
		}
	}
	return ""
}
