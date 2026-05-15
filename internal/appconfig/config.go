// Package appconfig loads app.yaml into a typed Config.
//
// It owns the rules for "where does the config live" and "where do all
// the runtime files live" — those decisions are concentrated here so
// the CLI commands can just take a *Config and not deal with paths.
package appconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the parsed app.yaml, with paths already expanded.
type Config struct {
	SubscriptionURL            string        `yaml:"subscription_url"`
	SubscriptionUpdateInterval time.Duration `yaml:"-"` // derived from seconds
	SubscriptionIntervalSec    int           `yaml:"subscription_update_interval"`

	TestURL         string `yaml:"test_url"`
	TestTimeoutMS   int    `yaml:"test_timeout"`
	TestConcurrency int    `yaml:"test_concurrency"`

	Priority []string `yaml:"priority"`

	XrayBin     string `yaml:"xray_bin"`
	XrayConfig  string `yaml:"xray_config"`
	XrayPort    int    `yaml:"xray_port"`
	XrayAPIPort int    `yaml:"xray_api_port"`

	PIDFile   string `yaml:"pid_file"`
	LogFile   string `yaml:"log_file"`
	CacheFile string `yaml:"cache_file"`
	StateFile string `yaml:"state_file"`
}

// TestTimeout returns TestTimeoutMS as a duration.
func (c *Config) TestTimeout() time.Duration {
	return time.Duration(c.TestTimeoutMS) * time.Millisecond
}

// Load reads the YAML at path, applies defaults and expands ~ in every
// path field. If path is empty, the standard locations are tried in
// order. Returns the resolved config plus the actual path it used.
func Load(path string) (*Config, string, error) {
	if path == "" {
		var err error
		path, err = defaultConfigPath()
		if err != nil {
			return nil, "", err
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, path, fmt.Errorf("appconfig: read %s: %w", path, err)
	}
	cfg := defaults()
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, path, fmt.Errorf("appconfig: parse %s: %w", path, err)
	}
	if err := cfg.normalize(); err != nil {
		return nil, path, err
	}
	return cfg, path, nil
}

// Locate returns the path Load would use without reading the file.
// Useful for `init`-style commands that want to know where to write.
func Locate() (string, error) { return defaultConfigPath() }

func defaultConfigPath() (string, error) {
	if v := os.Getenv("XRAY_WAYBAR_CONFIG"); v != "" {
		return expand(v)
	}
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("appconfig: locate config dir: %w", err)
	}
	return filepath.Join(cfgDir, "xray-waybar", "app.yaml"), nil
}

func defaults() *Config {
	return &Config{
		SubscriptionIntervalSec: 3600,
		TestURL:                 "http://www.gstatic.com/generate_204",
		TestTimeoutMS:           3000,
		TestConcurrency:         5,
		XrayBin:                 "/usr/bin/xray",
		XrayConfig:              "~/.config/xray-waybar/xray.json",
		XrayPort:                1080,
		XrayAPIPort:             8080,
		PIDFile:                 "/tmp/xray-waybar.pid",
		LogFile:                 "~/.local/share/xray-waybar/xray.log",
		CacheFile:               "~/.cache/xray-waybar/servers.json",
		StateFile:               "~/.cache/xray-waybar/state.json",
	}
}

func (c *Config) normalize() error {
	if c.SubscriptionURL == "" {
		return errors.New("appconfig: subscription_url is required")
	}
	if c.SubscriptionIntervalSec < 0 {
		return errors.New("appconfig: subscription_update_interval must be >= 0")
	}
	c.SubscriptionUpdateInterval = time.Duration(c.SubscriptionIntervalSec) * time.Second

	var err error
	for _, p := range []*string{&c.XrayConfig, &c.PIDFile, &c.LogFile, &c.CacheFile, &c.StateFile, &c.XrayBin} {
		*p, err = expand(*p)
		if err != nil {
			return err
		}
	}
	if c.TestConcurrency < 1 {
		c.TestConcurrency = 1
	}
	if c.TestTimeoutMS < 100 {
		c.TestTimeoutMS = 3000
	}
	return nil
}

// expand turns a leading "~" into the user's home directory. Anything
// else passes through unchanged. We intentionally do not call
// filepath.Abs — relative paths are useful for tests.
func expand(p string) (string, error) {
	if p == "" {
		return p, nil
	}
	if strings.HasPrefix(p, "~/") || p == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("appconfig: home dir: %w", err)
		}
		if p == "~" {
			return home, nil
		}
		return filepath.Join(home, p[2:]), nil
	}
	return p, nil
}
