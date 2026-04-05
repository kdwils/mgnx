package cmd

import (
	"fmt"
	"log"
	"net"

	"github.com/kdwils/mgnx/dht"
	"github.com/spf13/cobra"
)

var (
	externalIP string
)

// nodeidCmd represents the nodeid command
var nodeidCmd = &cobra.Command{
	Use:   "nodeid",
	Short: "Generate a node id from an external IP address",
	Long:  `Generate a node id from an external IP address`,
	Run: func(cmd *cobra.Command, args []string) {
		id, err := dht.DeriveBEP42NodeID(net.ParseIP(externalIP))
		if err != nil {
			log.Fatal(err)
		}

		fmt.Println(id.String())
	},
}

func init() {
	generateCmd.AddCommand(nodeidCmd)
	nodeidCmd.Flags().StringVar(&externalIP, "ip", "", "external ip to generate id from")
}
