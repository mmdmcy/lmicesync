package config

import (
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
)

// Config is the lmicesync configuration. Everything is overridable from the
// environment (LMICESYNC_*), which makes tests and deployments simple.
type Config struct {
	Device          string   `json:"device"`
	Token           string   `json:"token"`
	Roots           []string `json:"roots"`
	HTTPAddr        string   `json:"http_addr"`
	BeaconPort      int      `json:"beacon_port"`
	RequireApproval bool     `json:"require_approval"`
	ApprovalFile    string   `json:"approval_file"`
	SaveInterval    int      `json:"save_interval"`
	SyncInterval    int      `json:"sync_interval"`
	StateDir        string   `json:"state_dir"`
}

// DefaultPath is the user-level config location.
func DefaultPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.Getenv("HOME")
	}
	return filepath.Join(dir, "lmicesync", "config.json")
}

func defaultRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return []string{
		filepath.Join(home, "Documents", "github"),
		filepath.Join(home, "Documents", "gitlab"),
		filepath.Join(home, "Documents", "forgejo"),
	}
}

func hostname() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "device"
}

// Default returns a config with sane defaults.
func Default() *Config {
	home, _ := os.UserHomeDir()
	return &Config{
		Device:          hostname(),
		Token:           "",
		Roots:           defaultRoots(),
		HTTPAddr:        ":7370",
		BeaconPort:      10001,
		RequireApproval: true,
		ApprovalFile:    filepath.Join(os.Getenv("HOME"), ".config", "lmicesync", "approval.json"),
		SaveInterval:    15,
		SyncInterval:    5,
		StateDir:        filepath.Join(home, ".local", "state", "lmicesync"),
	}
}

// Load reads the config file at path (default DefaultPath) if present,
// applies defaults, then applies LMICESYNC_* environment overrides on top.
func Load(path string) (*Config, error) {
	cfg := Default()
	if path == "" {
		path = DefaultPath()
	}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("config %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	applyEnv(cfg)
	if cfg.Token == "" {
		// A token-less config still works for save; serve and sync will
		// refuse without one.
		cfg.Token = os.Getenv("LMICESYNC_TOKEN")
	}
	return cfg, nil
}

func applyEnv(cfg *Config) {
	if v := os.Getenv("LMICESYNC_DEVICE"); v != "" {
		cfg.Device = v
	}
	if v := os.Getenv("LMICESYNC_TOKEN"); v != "" {
		cfg.Token = v
	}
	if v := os.Getenv("LMICESYNC_ROOT"); v != "" {
		cfg.Roots = []string{v}
	}
	if v := os.Getenv("LMICESYNC_HTTP_ADDR"); v != "" {
		cfg.HTTPAddr = v
	}
	if v := os.Getenv("LMICESYNC_BEACON_PORT"); v != "" {
		fmt.Sscanf(v, "%d", &cfg.BeaconPort)
	}
	if v := os.Getenv("LMICESYNC_STATE_DIR"); v != "" {
		cfg.StateDir = v
	}
	if len(cfg.Roots) == 0 {
		cfg.Roots = defaultRoots()
	}
}

// Init writes a default config if none exists yet. Never overwrites.
func Init() error {
	path := DefaultPath()
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("config already exists: %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	cfg := Default()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
