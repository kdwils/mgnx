package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/kdwils/mgnx/config"
	"github.com/kdwils/mgnx/db"
	"github.com/kdwils/mgnx/db/gen"
	"github.com/kdwils/mgnx/indexer"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var indexCmd = &cobra.Command{
	Use:   "index",
	Short: "Reclassify all indexed torrents",
	Long:  `Reclassify all indexed torrents regardless of state. Use --dry-run to preview changes.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		dryRun, err := cmd.Flags().GetBool("dry-run")
		if err != nil {
			return err
		}

		cfg, err := config.New(viper.GetViper())
		if err != nil {
			return err
		}

		ctx := context.Background()
		pool, err := db.Connect(ctx, cfg.Database.URL)
		if err != nil {
			return err
		}
		defer pool.Close()

		ix := indexer.NewIndexer(gen.New(pool), cfg.Indexer)
		result, err := ix.Run(ctx, dryRun, os.Stdout)
		if err != nil {
			return err
		}

		if dryRun {
			fmt.Fprintf(os.Stdout, "\n[dry-run] scanned %d, would update %d\n", result.Scanned, result.Updated)
			return nil
		}
		fmt.Fprintf(os.Stdout, "scanned %d, updated %d\n", result.Scanned, result.Updated)
		return nil
	},
}

func init() {
	indexCmd.Flags().Bool("dry-run", false, "print changes without writing to the database")
	rootCmd.AddCommand(indexCmd)
}
