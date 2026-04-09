package config

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	Database Database `mapstructure:"database"`
	Server   Server   `mapstructure:"server"`
	DHT      DHT      `mapstructure:"dht"`
	Crawler  Crawler  `mapstructure:"crawler"`
	Indexer  Indexer  `mapstructure:"indexer"`
	Scrape   Scrape   `mapstructure:"scrape"`
}

type DHT struct {
	NodeID                 string        `mapstructure:"node_id"`
	Port                   int           `mapstructure:"port"`
	DiscoveryBuffer        int           `mapstructure:"discovery_buffer"`
	NodesPath              string        `mapstructure:"nodes_path"`
	GoodNodeWindow         time.Duration `mapstructure:"good_node_window"`
	BadFailureThreshold    int           `mapstructure:"bad_failure_threshold"`
	BucketSize             int           `mapstructure:"bucket_size"`
	StaleThreshold         time.Duration `mapstructure:"stale_threshold"`
	TransactionTimeout     time.Duration `mapstructure:"transaction_timeout"`
	TokenRotation          time.Duration `mapstructure:"token_rotation"`
	Workers                int           `mapstructure:"workers"`
	RateLimit              float64       `mapstructure:"rate_limit"`
	RateBurst              int           `mapstructure:"rate_burst"`
	MaxNodesPerResponse    int           `mapstructure:"max_nodes_per_response"`
	MaxPeersPerResponse    int           `mapstructure:"max_peers_per_response"`
	MaxMessageSize         int           `mapstructure:"max_message_size"`
	MaxMetadataSize        int           `mapstructure:"max_metadata_size"`
	ForwardedPortFile      string        `mapstructure:"forwarded_port_file"`
	ExternalIPFile         string        `mapstructure:"external_ip_file"`
	BootstrapRetryInterval time.Duration `mapstructure:"bootstrap_retry_interval"`
	FileWaitTimeout        time.Duration `mapstructure:"file_wait_timeout"`
}

type Crawler struct {
	Crawlers           int      `mapstructure:"crawlers"`
	DiscoveryWorkers   int      `mapstructure:"discovery_workers"`
	DiscoveryQueueSize int      `mapstructure:"discovery_queue_size"`
	BootstrapNodes     []string `mapstructure:"bootstrap_nodes"`
	Alpha              int      `mapstructure:"alpha"`
	MaxIterations      int      `mapstructure:"max_iterations"`
}

type Indexer struct {
	Workers               int           `mapstructure:"workers"`
	RateLimit             float64       `mapstructure:"rate_limit"`
	RateBurst             int           `mapstructure:"rate_burst"`
	RequestTimeout        time.Duration `mapstructure:"request_timeout"`
	MaxPeers              int           `mapstructure:"max_peers"`
	AllowedExtensions     []string      `mapstructure:"allowed_extensions"`
	EnableExtensionFilter bool          `mapstructure:"enable_extension_filter"`
	MinSize               int64         `mapstructure:"min_size"`
	MaxSize               int64         `mapstructure:"max_size"`
}

type Scrape struct {
	Workers       int           `mapstructure:"workers"`
	RateLimit     float64       `mapstructure:"rate_limit"`
	RateBurst     int           `mapstructure:"rate_burst"`
	BatchSize     int           `mapstructure:"batch_size"`
	PollInterval  time.Duration `mapstructure:"poll_interval"`
	DialTimeout   time.Duration `mapstructure:"dial_timeout"`
	ReadTimeout   time.Duration `mapstructure:"read_timeout"`
	DeadAfter     time.Duration `mapstructure:"dead_after"`
	PruneInterval time.Duration `mapstructure:"prune_interval"`
	Trackers      []string      `mapstructure:"trackers"`
}

type Database struct {
	URL string `mapstructure:"url"`
}

type Server struct {
	Port       int    `mapstructure:"port"`
	HealthPort int    `mapstructure:"health_port"`
	LogLevel   string `mapstructure:"log_level"`
	APIKey     string `mapstructure:"apiKey"`
}

func New(v *viper.Viper) (Config, error) {
	c := Config{}
	if v == nil {
		return c, errors.New("viper not initialized")
	}
	err := v.Unmarshal(&c)

	if c.DHT.ExternalIPFile != "" && c.DHT.NodeID != "" {
		return c, fmt.Errorf("dht.external_ip_file and dht.node_id are mutually exclusive")
	}

	return c, err
}
