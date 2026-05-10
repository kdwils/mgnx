package cmd

import (
	"fmt"
	"log"
	"net"

	"github.com/kdwils/mgnx/dht/table"
	"github.com/spf13/cobra"
)

var (
	externalIP string
	nodeID     string
)
var generateNodeIDCmd = &cobra.Command{
	Use:   "nodeid",
	Short: "Generate a BEP-42 compliant node id",
	Long:  `Generate a BEP-42 compliant node id from an external IP address`,
	Run: func(cmd *cobra.Command, args []string) {
		id, err := table.DeriveNodeIDFromIP(net.ParseIP(externalIP))
		if err != nil {
			log.Fatal(err)
		}

		fmt.Println(id.String())
	},
}

var validateNodeIDCmd = &cobra.Command{
	Use:   "nodeid",
	Short: "Validate a node ID against an IP address",
	Long:  `Validate that a node ID satisfies BEP-42 for the given IP address`,
	Run: func(cmd *cobra.Command, args []string) {
		parsedIP := net.ParseIP(externalIP)
		if parsedIP == nil {
			log.Fatalf("invalid IP address: %s", externalIP)
		}

		parsedID, err := table.ParseNodeIDHex(nodeID)
		if err != nil {
			log.Fatalf("invalid node ID: %v", err)
		}

		if err := table.ValidateNodeIDForIP(parsedIP, parsedID); err != nil {
			log.Fatalf("INVALID: %v", err)
		}

		fmt.Println("VALID")
	},
}

func init() {
	generateCmd.AddCommand(generateNodeIDCmd)
	generateNodeIDCmd.Flags().StringVar(&externalIP, "ip", "", "external ip to generate id from")

	validateCmd.AddCommand(validateNodeIDCmd)
	validateNodeIDCmd.Flags().StringVar(&externalIP, "ip", "", "external ip to generate id from")
	validateNodeIDCmd.Flags().StringVar(&nodeID, "id", "", "node ID to validate (40 hex characters)")
}
