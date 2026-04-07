package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "fialka",
	Short: "Fialka Mailbox — self-hosted store-and-forward relay",
	Long: `Fialka Mailbox is a self-hosted, privacy-first message relay server.
Messages are end-to-end encrypted by the Fialka app before deposit.
The server stores and forwards opaque blobs — it never sees plaintext.`,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(setupCmd)
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(restartCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(logsCmd)
	rootCmd.AddCommand(mailboxCmd)
}
