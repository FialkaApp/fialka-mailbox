package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
)

// Config holds all runtime configuration for Fialka Mailbox.
type Config struct {
	Server  ServerConfig  `mapstructure:"server"`
	Tor     TorConfig     `mapstructure:"tor"`
	Storage StorageConfig `mapstructure:"storage"`
	Limits  LimitsConfig  `mapstructure:"limits"`
	Log     LogConfig     `mapstructure:"log"`
}

type ServerConfig struct {
	Listen       string `mapstructure:"listen"`
	ExposeDirect bool   `mapstructure:"expose_direct"`
}

type TorConfig struct {
	Enabled     bool   `mapstructure:"enabled"`
	TorBinary   string `mapstructure:"tor_binary"`
	DataDir     string `mapstructure:"data_dir"`
	ControlNet  string `mapstructure:"control_net"`
	ControlAddr string `mapstructure:"control_addr"`
	CookieAuth  bool   `mapstructure:"cookie_auth"`
	Password    string `mapstructure:"password"`
}

type StorageConfig struct {
	DBPath string `mapstructure:"db_path"`
}

type LimitsConfig struct {
	MaxMessageSize          int64 `mapstructure:"max_message_size"`
	MaxMessagesPerRecipient int   `mapstructure:"max_messages_per_recipient"`
	MessageTTLHours         int   `mapstructure:"message_ttl_hours"`
	MaxStorageMB            int64 `mapstructure:"max_storage_mb"`
}

type LogConfig struct {
	Level  string `mapstructure:"level"`
	Pretty bool   `mapstructure:"pretty"`
}

// DefaultConfigPath returns the default config file path.
func DefaultConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "fialka-mailbox", "config.toml")
}

// Load reads and validates configuration from the given path.
// If path is empty, tries DefaultConfigPath then ./config.toml.
func Load(path string) (*Config, error) {
	v := viper.New()

	// Defaults
	v.SetDefault("server.listen", "127.0.0.1:7333") // port 7333 = Android HIDDEN_SERVICE_PORT
	v.SetDefault("server.expose_direct", false)
	v.SetDefault("tor.enabled", true)
	v.SetDefault("tor.control_net", "tcp")
	v.SetDefault("tor.control_addr", "127.0.0.1:9051")
	v.SetDefault("tor.cookie_auth", true)
	v.SetDefault("storage.db_path", "mailbox.db")
	v.SetDefault("limits.max_message_size", 65536)
	v.SetDefault("limits.max_messages_per_recipient", 100)
	v.SetDefault("limits.message_ttl_hours", 168)
	v.SetDefault("limits.max_storage_mb", 500)
	v.SetDefault("log.level", "info")
	v.SetDefault("log.pretty", false)

	v.SetConfigType("toml")

	if path != "" {
		v.SetConfigFile(path)
	} else {
		v.SetConfigFile(DefaultConfigPath())
		// Fallback to local ./config.toml if home config not found
		if _, err := os.Stat(DefaultConfigPath()); os.IsNotExist(err) {
			v.SetConfigFile("config.toml")
		}
	}

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			// No config file — use defaults only
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("reading config: %w", err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	return &cfg, nil
}

// Write serialises cfg to path in TOML format.
func Write(cfg *Config, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	v := viper.New()
	v.SetConfigType("toml")
	v.SetConfigFile(path)

	v.Set("server.listen", cfg.Server.Listen)
	v.Set("server.expose_direct", cfg.Server.ExposeDirect)
	v.Set("tor.enabled", cfg.Tor.Enabled)
	v.Set("tor.control_net", cfg.Tor.ControlNet)
	v.Set("tor.control_addr", cfg.Tor.ControlAddr)
	v.Set("tor.cookie_auth", cfg.Tor.CookieAuth)
	v.Set("tor.password", cfg.Tor.Password)
	v.Set("storage.db_path", cfg.Storage.DBPath)
	v.Set("limits.max_message_size", cfg.Limits.MaxMessageSize)
	v.Set("limits.max_messages_per_recipient", cfg.Limits.MaxMessagesPerRecipient)
	v.Set("limits.message_ttl_hours", cfg.Limits.MessageTTLHours)
	v.Set("limits.max_storage_mb", cfg.Limits.MaxStorageMB)
	v.Set("log.level", cfg.Log.Level)
	v.Set("log.pretty", cfg.Log.Pretty)

	return v.WriteConfigAs(path)
}
