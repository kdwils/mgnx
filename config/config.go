package config

import (
	"errors"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	Database Database `mapstructure:"database"`
	Server   Server   `mapstructure:"server"`
	DHT      DHT      `mapstructure:"dht"`
	Indexer  Indexer  `mapstructure:"indexer"`
	Scrape   Scrape   `mapstructure:"scrape"`
}

type DHT struct {
	NodeID              string        `mapstructure:"node_id"`
	Port                int           `mapstructure:"port"`                  // default 6881
	BootstrapNodes      []string      `mapstructure:"bootstrap_nodes"`       // override defaults
	RateLimit           float64       `mapstructure:"rate_limit"`            // queries/sec, default 25
	RateBurst           int           `mapstructure:"rate_burst"`            // default 25
	Workers             int           `mapstructure:"workers"`               // query handler goroutines, default 4
	BEP51Workers        int           `mapstructure:"bep51_workers"`         // default 2
	DiscoveryBuffer     int           `mapstructure:"discovery_buffer"`      // channel size, default 10000
	NodesPath           string        `mapstructure:"nodes_path"`            // default $HOME/.mgnx/dht_nodes.dat
	GoodNodeWindow      time.Duration `mapstructure:"good_node_window"`      // default 15m
	BadFailureThreshold int           `mapstructure:"bad_failure_threshold"` // default 2
	BucketSize          int           `mapstructure:"bucket_size"`           // default 8 (BEP-05 k)
	StaleThreshold      time.Duration `mapstructure:"stale_threshold"`       // default 15m
	TransactionTimeout  time.Duration `mapstructure:"transaction_timeout"`   // default 10s
	TokenRotation       time.Duration `mapstructure:"token_rotation"`        // default 5m
}

type Indexer struct {
	Workers            int           `mapstructure:"workers"`
	FetchTimeout       time.Duration `mapstructure:"fetch_timeout"`
	ExcludedExtensions []string      `mapstructure:"excluded_extensions"`
}

type Scrape struct {
	Workers       int           `mapstructure:"workers"`        // concurrent tracker connections, default 5
	BatchSize     int           `mapstructure:"batch_size"`     // infohashes per scrape request, default 74
	PollInterval  time.Duration `mapstructure:"poll_interval"`  // how often to check for due scrapes, default 30s
	DialTimeout   time.Duration `mapstructure:"dial_timeout"`   // UDP dial timeout, default 5s
	ReadTimeout   time.Duration `mapstructure:"read_timeout"`   // UDP read deadline, default 10s
	DeadAfter     time.Duration `mapstructure:"dead_after"`     // mark dead after seeders=0 this long, default 90d
	PruneInterval time.Duration `mapstructure:"prune_interval"` // how often to prune scrape_history, default 24h
	Trackers      []string      `mapstructure:"trackers"`       // UDP tracker URLs
}

type Database struct {
	URI string `mapstructure:"uri"`
}

type Server struct {
	Port     int    `mapstructure:"port"`
	LogLevel string `mapstructure:"log_level"`
	APIKey   string `mapstructure:"apiKey"`
}

func New(v *viper.Viper) (Config, error) {
	c := Config{}
	if v == nil {
		return c, errors.New("viper not initialized")
	}
	err := v.Unmarshal(&c)
	return c, err
}
