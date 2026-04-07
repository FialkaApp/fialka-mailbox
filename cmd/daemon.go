package cmd

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/fialkaapp/fialka-mailbox/internal/config"
	"github.com/fialkaapp/fialka-mailbox/internal/storage"
	"github.com/fialkaapp/fialka-mailbox/internal/tor"
	"github.com/fialkaapp/fialka-mailbox/internal/transport"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var cfgPath string

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the Fialka Mailbox daemon",
	RunE:  runStart,
}

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the Fialka Mailbox daemon (send SIGTERM)",
	RunE: func(cmd *cobra.Command, args []string) error {
		log.Info().Msg("Send SIGTERM to the running fialka-mailbox process.")
		return nil
	},
}

var restartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the daemon (stop then start)",
	RunE: func(cmd *cobra.Command, args []string) error {
		log.Info().Msg("stop the process, then run 'fialka start'.")
		return nil
	},
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show Fialka Mailbox status",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(cfgPath)
		if err != nil {
			return err
		}
		store, err := storage.NewSQLiteStore(cfg.Storage.DBPath)
		if err != nil {
			return err
		}
		defer store.Close()
		stats, err := store.Stats()
		if err != nil {
			return err
		}
		log.Info().
			Int64("pending_messages", stats.PendingMessages).
			Int64("recipients", stats.Recipients).
			Int64("total_size_bytes", stats.TotalSizeBytes).
			Msg("status")
		return nil
	},
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Print the active configuration path",
	RunE: func(cmd *cobra.Command, args []string) error {
		path := cfgPath
		if path == "" {
			path = config.DefaultConfigPath()
		}
		log.Info().Str("config", path).Msg("config file")
		return nil
	},
}

var logsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Show the log file path",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(cfgPath)
		if err != nil {
			return err
		}
		logFile := filepath.Join(filepath.Dir(cfg.Storage.DBPath), "fialka-mailbox.log")
		log.Info().Str("log_file", logFile).Msg("logs location")
		return nil
	},
}

func init() {
	startCmd.Flags().StringVarP(&cfgPath, "config", "c", "", "config file path (default: ~/.config/fialka-mailbox/config.toml)")
	statusCmd.Flags().StringVarP(&cfgPath, "config", "c", "", "config file path")
}

func runStart(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}

	// Configure logger
	level, _ := zerolog.ParseLevel(cfg.Log.Level)
	zerolog.SetGlobalLevel(level)
	if cfg.Log.Pretty {
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})
	}

	log.Info().Str("version", "0.2.0").Msg("Fialka Mailbox starting")

	// Open storage
	store, err := storage.NewSQLiteStore(cfg.Storage.DBPath)
	if err != nil {
		return err
	}
	defer store.Close()
	log.Info().Str("db", cfg.Storage.DBPath).Msg("storage opened")

	// Start TTL expiry ticker
	go runExpiry(store)

	// Start Tor hidden service
	var torCtrl *tor.Controller
	if cfg.Tor.Enabled {
		torCtrl, err = connectTor(cfg)
		if err != nil {
			log.Warn().Err(err).Msg("Tor unavailable — running without hidden service")
		} else {
			defer torCtrl.Close()
			// Persist onion address for invite links and CLI commands
			_ = store.SetMeta("onion_address", torCtrl.OnionAddress)
			log.Info().Str("onion", torCtrl.OnionAddress).Msg("hidden service ready")
			log.Info().Msg(torCtrl.OnionAddressQR())
		}
	}

	// Graceful shutdown context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		<-sigCh
		log.Info().Msg("shutting down…")
		cancel()
	}()

	// Start TorTransport TCP server on port 7333
	// Android always connects to port 7333 (HIDDEN_SERVICE_PORT).
	// Tor maps external:7333 → 127.0.0.1:7333 via ADD_ONION.
	srv := transport.New(store, cfg, log.Logger)
	if err := srv.ListenAndServe(ctx, cfg.Server.Listen); err != nil {
		return err
	}

	<-ctx.Done()
	log.Info().Msg("Fialka Mailbox stopped")
	return nil
}

func runExpiry(store *storage.SQLiteStore) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		n, err := store.Expire()
		if err != nil {
			log.Error().Err(err).Msg("expiry failed")
		} else if n > 0 {
			log.Info().Int("expired", n).Msg("messages expired")
		}
	}
}

func connectTor(cfg *config.Config) (*tor.Controller, error) {
	dataDir := cfg.Tor.DataDir
	if dataDir == "" {
		home, _ := os.UserHomeDir()
		dataDir = filepath.Join(home, ".config", "fialka-mailbox", "tor")
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, err
	}

	ctrl, err := tor.Connect(
		cfg.Tor.ControlNet,
		cfg.Tor.ControlAddr,
		cfg.Tor.Password,
		dataDir,
		cfg.Tor.CookieAuth,
	)
	if err != nil {
		return nil, err
	}

	// Map hidden service port 7333 → local TCP server (cfg.Server.Listen = 127.0.0.1:7333)
	// Android HIDDEN_SERVICE_PORT = 7333 — must match exactly.
	if err := ctrl.CreateHiddenService(7333, cfg.Server.Listen); err != nil {
		ctrl.Close()
		return nil, err
	}

	return ctrl, nil
}
