package cmd

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfgFile string

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "mgnx",
	Short: "A brief description of your application",
	Long: `A longer description that spans multiple lines and likely contains
examples and usage of using your application. For example:

Cobra is a CLI library for Go that empowers applications.
This application is a tool to generate the needed files
to quickly create a Cobra application.`,
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.mgnx.yaml)")
	rootCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Printf("config: could not determine home directory: %v", err)
	}

	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		if home != "" {
			viper.AddConfigPath(home)
		}
		viper.AddConfigPath(".")
		viper.SetConfigName(".mgnx")
		viper.SetConfigType("yaml")
	}

	viper.SetDefault("dht.bootstrap_nodes", []string{
		"router.bittorrent.com:6881",
		"router.utorrent.com:6881",
		"dht.transmissionbt.com:6881",
	})
	viper.SetDefault("dht.port", 6881)
	viper.SetDefault("dht.rate_limit", 25.0)
	viper.SetDefault("dht.rate_burst", 25)
	viper.SetDefault("dht.workers", 4)
	viper.SetDefault("dht.bep51_workers", 2)
	viper.SetDefault("dht.discovery_buffer", 10000)
	viper.SetDefault("dht.node_id_path", filepath.Join(home, ".mgnx", "dht_id"))
	viper.SetDefault("dht.nodes_path", filepath.Join(home, ".mgnx", "dht_nodes.dat"))
	viper.SetDefault("dht.good_node_window", 15*time.Minute)
	viper.SetDefault("dht.bad_failure_threshold", 2)
	viper.SetDefault("dht.bucket_size", 8)
	viper.SetDefault("dht.stale_threshold", 15*time.Minute)
	viper.SetDefault("dht.transaction_timeout", 10*time.Second)
	viper.SetDefault("dht.token_rotation", 5*time.Minute)

	viper.SetDefault("scrape.workers", 5)
	viper.SetDefault("scrape.batch_size", 74)
	viper.SetDefault("scrape.poll_interval", 30*time.Second)
	viper.SetDefault("scrape.dial_timeout", 5*time.Second)
	viper.SetDefault("scrape.read_timeout", 10*time.Second)
	viper.SetDefault("scrape.dead_after", 90*24*time.Hour)
	viper.SetDefault("scrape.prune_interval", 24*time.Hour)
	viper.SetDefault("scrape.trackers", []string{
		"udp://tracker.opentrackr.org:1337/announce",
		"udp://open.tracker.cl:1337/announce",
		"udp://tracker.openbittorrent.com:6969/announce",
		"udp://exodus.desync.com:6969/announce",
	})

	viper.SetEnvPrefix("mgnx")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", ""))
	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err != nil {
		log.Printf("config: %v", err)
	}
}
