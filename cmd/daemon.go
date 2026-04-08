package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fialkaapp/fialka-mailbox/internal/config"
	"github.com/fialkaapp/fialka-mailbox/internal/storage"
	"github.com/fialkaapp/fialka-mailbox/internal/tor"
	"github.com/fialkaapp/fialka-mailbox/internal/transport"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var cfgPath string
var passphraseStdin bool

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
	startCmd.Flags().BoolVar(&passphraseStdin, "passphrase-stdin", false, "read passphrase from stdin (installer use only)")
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

	// Resolve passphrase before opening any services (fail fast if wrong)
	var passphrase []byte
	if cfg.Tor.Enabled && cfg.Tor.KeyProtection == "passphrase" {
		dataDir := resolveDataDir(cfg)
		passphrase, err = acquirePassphrase(passphraseStdin)
		if err != nil {
			return fmt.Errorf("acquiring passphrase: %w", err)
		}
		if tor.IsKeyEncrypted(dataDir) {
			// Validate immediately — wrong passphrase = hard fail before starting storage
			testKey, decErr := tor.DecryptKey(dataDir, passphrase)
			if decErr != nil {
				zeroBytes(passphrase)
				return fmt.Errorf("passphrase incorrect: %w", decErr)
			}
			zeroBytes([]byte(testKey))
			log.Info().Msg("passphrase verified — onion key decrypted successfully")
		}
	}

	// Open storage
	store, err := storage.NewSQLiteStore(cfg.Storage.DBPath)
	if err != nil {
		zeroBytes(passphrase)
		return err
	}
	defer store.Close()
	log.Info().Str("db", cfg.Storage.DBPath).Msg("storage opened")

	// Start TTL expiry ticker
	go runExpiry(store)

	// Start Tor hidden service
	var torCtrl *tor.Controller
	if cfg.Tor.Enabled {
		torCtrl, err = connectTor(cfg, passphrase)
		zeroBytes(passphrase) // zeroed immediately after key handed to Tor
		if err != nil {
			log.Warn().Err(err).Msg("Tor unavailable — running without hidden service")
		} else {
			defer torCtrl.Close()
			_ = store.SetMeta("onion_address", torCtrl.OnionAddress)
			log.Info().Str("onion", torCtrl.OnionAddress).Msg("hidden service ready")
			log.Info().Msg(torCtrl.OnionAddressQR())
		}
	} else {
		zeroBytes(passphrase)
	}

	// Graceful shutdown context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		<-sigCh
		log.Info().Msg("shutting down...")
		cancel()
	}()

	// Start TorTransport TCP server
	srv := transport.New(store, cfg, log.Logger)

	// Detect interactive terminal: launch TUI; otherwise run headless (systemd, pipe).
	if term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
		// Redirect zerolog into the TUI viewport — server goroutines never write to stdout.
		logCh := make(chan string, 512)
		logWriter := &tuiLogWriter{ch: logCh}
		log.Logger = log.Output(zerolog.ConsoleWriter{
			Out:        logWriter,
			TimeFormat: "15:04:05",
			NoColor:    true,
		})

		onionAddr := ""
		if torCtrl != nil {
			onionAddr = torCtrl.OnionAddress
		}

		// Server runs in background; TUI drives the main goroutine.
		go func() { _ = srv.ListenAndServe(ctx, cfg.Server.Listen) }()

		model := newTUIModel(onionAddr, store, cancel, logCh, cfgPath)
		p := tea.NewProgram(model, tea.WithAltScreen())
		_, runErr := p.Run()
		cancel() // ensure server goroutine stops when TUI exits
		if runErr != nil {
			return runErr
		}
		return nil
	}

	// Headless mode (systemd service, pipe): block until signal.
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

// resolveDataDir returns the effective Tor data directory.
func resolveDataDir(cfg *config.Config) string {
	if cfg.Tor.DataDir != "" {
		return cfg.Tor.DataDir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "fialka-mailbox", "tor")
}

// acquirePassphrase reads the passphrase either from stdin (--passphrase-stdin)
// or interactively via systemd-ask-password / /dev/tty.
func acquirePassphrase(fromStdin bool) ([]byte, error) {
	if fromStdin {
		scanner := bufio.NewScanner(os.Stdin)
		if scanner.Scan() {
			line := strings.TrimRight(scanner.Text(), "\r\n")
			if line == "" {
				return nil, fmt.Errorf("passphrase is empty")
			}
			return []byte(line), nil
		}
		return nil, fmt.Errorf("no passphrase received on stdin")
	}

	// Prefer systemd-ask-password (works from TTY, Plymouth, systemd agent)
	if _, err := exec.LookPath("systemd-ask-password"); err == nil {
		out, err := exec.Command("systemd-ask-password",
			"--timeout=300",
			"Fialka Mailbox -- Enter onion key passphrase:").Output()
		if err == nil {
			pass := strings.TrimRight(string(out), "\r\n")
			if pass == "" {
				return nil, fmt.Errorf("passphrase is empty")
			}
			return []byte(pass), nil
		}
		// fall through to direct TTY prompt
	}

	// Open /dev/tty explicitly so it works even when stdout/stderr go to journald
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		tty = os.Stdin
	} else {
		defer tty.Close()
	}

	fmt.Fprint(tty, "Fialka Mailbox -- Enter onion key passphrase: ")
	pass, err := term.ReadPassword(int(tty.Fd()))
	fmt.Fprintln(tty)
	if err != nil {
		return nil, fmt.Errorf("reading passphrase: %w", err)
	}
	if len(pass) == 0 {
		return nil, fmt.Errorf("passphrase is empty")
	}
	return pass, nil
}

// connectTor connects to the Tor control port and registers the hidden service.
// passphrase is only used in "passphrase" mode and must be zeroed by the caller after.
func connectTor(cfg *config.Config, passphrase []byte) (*tor.Controller, error) {
	dataDir := resolveDataDir(cfg)
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
		return nil, fmt.Errorf("connecting to Tor control port: %w", err)
	}

	privKey, err := resolveOnionKey(cfg, dataDir, passphrase)
	if err != nil {
		ctrl.Close()
		return nil, err
	}

	// Android HIDDEN_SERVICE_PORT = 7333 -- must match exactly.
	if err := ctrl.CreateHiddenService(7333, cfg.Server.Listen, privKey); err != nil {
		ctrl.Close()
		return nil, fmt.Errorf("creating hidden service: %w", err)
	}
	zeroBytes([]byte(privKey))

	// Persist a newly generated key with the appropriate protection scheme.
	if ctrl.OnionPrivKey != "" {
		if err := saveOnionKey(cfg, dataDir, ctrl.OnionPrivKey, passphrase); err != nil {
			log.Warn().Err(err).Msg("could not persist onion key -- restart will generate a new .onion address!")
		} else {
			log.Info().Str("protection", cfg.Tor.KeyProtection).Msg("new onion key persisted")
		}
		zeroBytes([]byte(ctrl.OnionPrivKey))
		ctrl.OnionPrivKey = ""
	}

	return ctrl, nil
}

// resolveOnionKey loads the onion private key using the configured protection mode.
// Returns "" if no key exists yet (first run).
func resolveOnionKey(cfg *config.Config, dataDir string, passphrase []byte) (string, error) {
	switch cfg.Tor.KeyProtection {

	case "tpm":
		// systemd decrypts via TPM2 and exposes the credential in $CREDENTIALS_DIRECTORY.
		credsDir := os.Getenv("CREDENTIALS_DIRECTORY")
		if credsDir == "" {
			log.Warn().Msg("key_protection=tpm but CREDENTIALS_DIRECTORY not set -- falling back to plaintext")
			return tor.LoadPlaintextKey(dataDir)
		}
		credName := cfg.Tor.CredName
		if credName == "" {
			credName = "onion-key"
		}
		raw, err := os.ReadFile(filepath.Join(credsDir, credName))
		if err != nil {
			return "", fmt.Errorf("reading TPM credential %q: %w", credName, err)
		}
		key := strings.TrimSpace(string(raw))
		zeroBytes(raw)
		return key, nil

	case "passphrase":
		if !tor.IsKeyEncrypted(dataDir) {
			return "", nil // first run -- Tor generates new key
		}
		if len(passphrase) == 0 {
			return "", fmt.Errorf("key_protection=passphrase but no passphrase provided")
		}
		return tor.DecryptKey(dataDir, passphrase)

	default: // "plaintext" or empty
		return tor.LoadPlaintextKey(dataDir)
	}
}

// saveOnionKey persists a newly generated onion key with the right scheme.
func saveOnionKey(cfg *config.Config, dataDir string, privKey string, passphrase []byte) error {
	switch cfg.Tor.KeyProtection {
	case "tpm":
		// Save plaintext first; installer's post-init step runs systemd-creds to encrypt it.
		if os.Getenv("CREDENTIALS_DIRECTORY") != "" {
			return nil // already running under systemd credentials
		}
		return tor.SavePlaintextKey(dataDir, privKey)
	case "passphrase":
		if len(passphrase) == 0 {
			return fmt.Errorf("cannot encrypt key: passphrase is empty")
		}
		return tor.EncryptAndSaveKey(dataDir, privKey, passphrase)
	default:
		return tor.SavePlaintextKey(dataDir, privKey)
	}
}

// zeroBytes overwrites a byte slice with zeroes to reduce key material lifetime in memory.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
