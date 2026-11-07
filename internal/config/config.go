package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config holds the settings the TUI and CLI share.
type Config struct {
	Dir            string `json:"dir,omitempty"`
	Conns          int    `json:"connections,omitempty"`
	Rate           string `json:"rate,omitempty"`
	GlobalRate     string `json:"global_rate,omitempty"`
	MaxConcurrent  int    `json:"max_concurrent,omitempty"`
	HistoryMax     int    `json:"history_max,omitempty"`
	Theme          string `json:"theme,omitempty"`
	CookiesFromZen bool   `json:"cookies_from_zen,omitempty"`
	ClipboardAuto  bool   `json:"clipboard_auto,omitempty"`
}

// Default returns settings used when no config file exists.
func Default() *Config {
	return &Config{
		Dir:           defaultDownloadDir(),
		Conns:         16,
		MaxConcurrent: 3,
		HistoryMax:    200,
		Theme:         "auto",
	}
}

func defaultDownloadDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, "Downloads")
}

// Path is the platform-appropriate config file location.
func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "zdl", "config.json"), nil
}

// HistoryPath is where the dashboard keeps its download list across sessions.
func HistoryPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "zdl", "history.json"), nil
}

// Load merges the file over the defaults. A missing or broken file is not
// fatal: the defaults are returned with err set for diagnostics.
func Load() (*Config, error) {
	cfg := Default()
	path, err := Path()
	if err != nil {
		return cfg, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return Default(), fmt.Errorf("config %s: %w", path, err)
	}
	if strings.TrimSpace(cfg.Rate) != "" {
		if _, err := ParseSize(cfg.Rate); err != nil {
			return Default(), fmt.Errorf("config %s: rate: %w", path, err)
		}
	}
	cfg.Normalize()
	return cfg, nil
}

func (c *Config) Normalize() {
	if c.Conns < 1 {
		c.Conns = 16
	}
	if c.Conns > 64 {
		c.Conns = 64
	}
	if c.MaxConcurrent < 1 {
		c.MaxConcurrent = 3
	}
	if c.MaxConcurrent > 16 {
		c.MaxConcurrent = 16
	}
	if c.HistoryMax < 1 {
		c.HistoryMax = 200
	}
	if c.Theme != "dark" && c.Theme != "light" && c.Theme != "auto" {
		c.Theme = "auto"
	}
	if c.Dir == "" {
		c.Dir = defaultDownloadDir()
	}
}

// ThemeMode returns the resolved theme: "dark", "light", or "auto".
func (c *Config) ThemeMode() string {
	return c.Theme
}

// GlobalRateBytes parses the human-readable global rate ("8M", "500K"); 0 means unlimited.
func (c *Config) GlobalRateBytes() int64 {
	n, err := ParseSize(c.GlobalRate)
	if err != nil {
		return 0
	}
	return n
}

func (c *Config) Save() error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// RateBytes parses the human-readable rate ("8M", "500K"); 0 means unlimited.
func (c *Config) RateBytes() int64 {
	n, err := ParseSize(c.Rate)
	if err != nil {
		return 0
	}
	return n
}

// ParseSize understands 10M, 500K, 1.5G, 800KB, and plain bytes.
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0, nil
	}
	mult := int64(1)
	suffixes := []struct {
		suf string
		m   int64
	}{
		{"KIB", 1 << 10}, {"MIB", 1 << 20}, {"GIB", 1 << 30},
		{"KB", 1000}, {"MB", 1000 * 1000}, {"GB", 1000 * 1000 * 1000},
		{"K", 1 << 10}, {"M", 1 << 20}, {"G", 1 << 30}, {"B", 1},
	}
	for _, x := range suffixes {
		if strings.HasSuffix(s, x.suf) {
			mult = x.m
			s = strings.TrimSpace(strings.TrimSuffix(s, x.suf))
			break
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("bad size %q", s)
	}
	return int64(v * float64(mult)), nil
}
