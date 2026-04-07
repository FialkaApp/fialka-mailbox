package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/charmbracelet/huh"
	"github.com/fialkaapp/fialka-mailbox/internal/config"
	"github.com/spf13/cobra"
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Interactive setup wizard",
	Long:  "Run the onboarding wizard to configure Fialka Mailbox for the first time.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSetup()
	},
}

func runSetup() error {
	fmt.Println("\n  Fialka Mailbox — Setup Wizard")
	fmt.Println()

	cfg := &config.Config{
		Server:  config.ServerConfig{Listen: "127.0.0.1:7333"},
		Tor:     config.TorConfig{Enabled: true, ControlNet: "tcp", ControlAddr: "127.0.0.1:9051", CookieAuth: true},
		Storage: config.StorageConfig{},
		Limits: config.LimitsConfig{
			MaxMessageSize:          65536,
			MaxMessagesPerRecipient: 100,
			MessageTTLHours:         168,
			MaxStorageMB:            500,
		},
		Log: config.LogConfig{Level: "info", Pretty: true},
	}

	home, _ := os.UserHomeDir()
	defaultDataDir := filepath.Join(home, ".config", "fialka-mailbox")
	dataDir := defaultDataDir
	listenPort := "8765"
	torEnabled := true
	torControlAddr := "127.0.0.1:9051"
	ttlDays := "7"
	maxMsgKB := "64"
	maxMsgsPerRecip := "100"
	maxStorageMB := "500"

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewNote().
				Title("Welcome to Fialka Mailbox").
				Description("This wizard configures your self-hosted store-and-forward relay.\nMessages are end-to-end encrypted by the Fialka app — this server\nnever sees plaintext.\n"),
		),

		huh.NewGroup(
			huh.NewInput().
				Title("Data directory").
				Description("Where to store the database and Tor keys.").
				Placeholder(defaultDataDir).
				Value(&dataDir),

			huh.NewInput().
				Title("HTTP listen port").
				Description("Internal port (only reachable via Tor, not exposed publicly).").
				Placeholder("8765").
				Value(&listenPort).
				Validate(func(s string) error {
					p, err := strconv.Atoi(s)
					if err != nil || p < 1 || p > 65535 {
						return fmt.Errorf("enter a valid port (1–65535)")
					}
					return nil
				}),
		),

		huh.NewGroup(
			huh.NewConfirm().
				Title("Enable Tor hidden service?").
				Description("Exposes the mailbox as a .onion address.\nRequires a running Tor daemon (recommended).").
				Value(&torEnabled),
		),

		huh.NewGroup(
			huh.NewInput().
				Title("Tor control port address").
				Placeholder("127.0.0.1:9051").
				Value(&torControlAddr),
		).WithHideFunc(func() bool { return !torEnabled }),

		huh.NewGroup(
			huh.NewInput().
				Title("Message TTL (days)").
				Description("Messages older than this are automatically deleted.").
				Placeholder("7").
				Value(&ttlDays).
				Validate(func(s string) error {
					d, err := strconv.Atoi(s)
					if err != nil || d < 1 {
						return fmt.Errorf("must be a positive integer")
					}
					return nil
				}),

			huh.NewInput().
				Title("Max message size (KB)").
				Description("Maximum size of a single encrypted message.").
				Placeholder("64").
				Value(&maxMsgKB).
				Validate(func(s string) error {
					n, err := strconv.Atoi(s)
					if err != nil || n < 1 {
						return fmt.Errorf("must be a positive integer")
					}
					return nil
				}),

			huh.NewInput().
				Title("Max messages per recipient").
				Placeholder("100").
				Value(&maxMsgsPerRecip).
				Validate(func(s string) error {
					n, err := strconv.Atoi(s)
					if err != nil || n < 1 {
						return fmt.Errorf("must be a positive integer")
					}
					return nil
				}),

			huh.NewInput().
				Title("Max total storage (MB)").
				Placeholder("500").
				Value(&maxStorageMB).
				Validate(func(s string) error {
					n, err := strconv.Atoi(s)
					if err != nil || n < 1 {
						return fmt.Errorf("must be a positive integer")
					}
					return nil
				}),
		),
	)

	if err := form.Run(); err != nil {
		return fmt.Errorf("setup aborted: %w", err)
	}

	// Apply collected values
	if dataDir == "" {
		dataDir = defaultDataDir
	}
	if listenPort == "" {
		listenPort = "8765"
	}

	cfg.Server.Listen = "127.0.0.1:" + listenPort
	cfg.Tor.Enabled = torEnabled
	cfg.Tor.ControlAddr = torControlAddr
	cfg.Storage.DBPath = filepath.Join(dataDir, "mailbox.db")

	ttlDaysInt, _ := strconv.Atoi(ttlDays)
	cfg.Limits.MessageTTLHours = ttlDaysInt * 24

	maxMsgKBInt, _ := strconv.Atoi(maxMsgKB)
	cfg.Limits.MaxMessageSize = int64(maxMsgKBInt) * 1024

	maxMsgsPerRecipInt, _ := strconv.Atoi(maxMsgsPerRecip)
	cfg.Limits.MaxMessagesPerRecipient = maxMsgsPerRecipInt

	maxStorageMBInt, _ := strconv.Atoi(maxStorageMB)
	cfg.Limits.MaxStorageMB = int64(maxStorageMBInt)

	// Write config
	configPath := filepath.Join(dataDir, "config.toml")
	if err := config.Write(cfg, configPath); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	fmt.Printf("\n  ✓ Config written to: %s\n", configPath)
	fmt.Printf("  ✓ Database will be created at: %s\n", cfg.Storage.DBPath)
	if torEnabled {
		fmt.Printf("  ✓ Tor hidden service will be created on first start\n")
	}
	fmt.Printf("\n  Run 'fialka start --config %s' to launch.\n\n", configPath)
	return nil
}
