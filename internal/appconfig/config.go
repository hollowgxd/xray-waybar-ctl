// Package appconfig loads app.yaml into a typed Config.
//
// It owns the rules for "where does the config live" and "where do all
// the runtime files live" — those decisions are concentrated here so
// the CLI commands can just take a *Config and not deal with paths.
package appconfig

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the parsed app.yaml, with paths already expanded.
type Config struct {
	// Core selects the runtime backend. "xray" preserves the original
	// one-server-per-process behaviour; "mihomo" runs the complete
	// subscription as a native Mihomo profile and controls it over the
	// localhost REST API.
	Core string `yaml:"core"`

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

	MihomoBin              string `yaml:"mihomo_bin"`
	MihomoHome             string `yaml:"mihomo_home"`
	MihomoConfig           string `yaml:"mihomo_config"`
	MihomoSubscriptionFile string `yaml:"mihomo_subscription_file"`
	MihomoSecretFile       string `yaml:"mihomo_secret_file"`
	MihomoController       string `yaml:"mihomo_controller"`
	MihomoGroup            string `yaml:"mihomo_group"`
	MihomoTUNStack         string `yaml:"mihomo_tun_stack"`

	PIDFile   string `yaml:"pid_file"`
	LogFile   string `yaml:"log_file"`
	CacheFile string `yaml:"cache_file"`
	StateFile string `yaml:"state_file"`
	HWIDFile  string `yaml:"hwid_file"`

	// SystemWide enables the external tun2socks sidecar for Xray or the
	// native TUN/auto-route stack for Mihomo.
	SystemWide bool `yaml:"system_wide"`

	// RoutingProfile chooses how xrayconfig.Generate populates routing.rules.
	// Two built-ins (no download, no geo files needed):
	//   - "proxy-all" — empty rules, everything goes via the proxy outbound.
	//   - "direct"    — everything via direct (kill switch / debug).
	// Two URL-backed presets (resolved via RulesPresets):
	//   - "smart"     — hydraponique HAPP/DEFAULT.JSON  (curated RU bypass)
	//   - "whitelist" — hydraponique HAPP/WHITELIST.JSON (stricter)
	// Or a raw https:// URL to any HAPP-shaped rules JSON, which is
	// downloaded to <geo.dir>/rules.json and read on launch.
	// May be overridden at runtime via `xray-waybar-ctl profile <name>`
	// (writes to state.json and persists across restarts).
	RoutingProfile string `yaml:"routing_profile"`

	// Geo controls where geoip.dat / geosite.dat live and where to
	// download them from. The directory is exported to xray as
	// XRAY_LOCATION_ASSET on launch.
	Geo GeoConfig `yaml:"geo"`
}

// GeoConfig is the geoip/geosite asset configuration.
type GeoConfig struct {
	Dir              string        `yaml:"dir"`
	GeoipURL         string        `yaml:"geoip_url"`
	GeositeURL       string        `yaml:"geosite_url"`
	RefreshInterval  time.Duration `yaml:"-"`
	RefreshIntervalS int           `yaml:"refresh_interval"`
}

// RulesPresets maps short names to canonical rules-JSON URLs published
// by hydraponique/roscomvpn-routing. These pair with the matching
// geoip.dat / geosite.dat that GeoipURL / GeositeURL point at.
var RulesPresets = map[string]string{
	"smart":     "https://raw.githubusercontent.com/hydraponique/roscomvpn-routing/main/HAPP/DEFAULT.JSON",
	"whitelist": "https://raw.githubusercontent.com/hydraponique/roscomvpn-routing/main/HAPP/WHITELIST.JSON",
}

// BuiltinProfiles are profile names that require no rules-JSON download.
// They are rendered entirely from code in xrayconfig.
var BuiltinProfiles = []string{"proxy-all", "direct"}

// ProfileRulesURL returns the rules-JSON URL for a profile name. Returns:
//   - the preset URL when name is in RulesPresets
//   - the name itself when it already looks like https:// URL
//   - "" for built-in or unknown profiles
func ProfileRulesURL(name string) string {
	if u, ok := RulesPresets[name]; ok {
		return u
	}
	if strings.HasPrefix(name, "https://") || strings.HasPrefix(name, "http://") {
		return name
	}
	return ""
}

// IsBuiltinProfile reports whether the profile renders without a
// downloaded rules-JSON (proxy-all / direct).
func IsBuiltinProfile(name string) bool {
	for _, b := range BuiltinProfiles {
		if b == name {
			return true
		}
	}
	return false
}

// ValidateProfile returns nil if name is a known profile (built-in,
// preset, or http(s) URL). Used to reject typos at config load and
// at `profile <name>`.
func ValidateProfile(name string) error {
	if IsBuiltinProfile(name) {
		return nil
	}
	if _, ok := RulesPresets[name]; ok {
		return nil
	}
	if strings.HasPrefix(name, "https://") || strings.HasPrefix(name, "http://") {
		return nil
	}
	allowed := append([]string{}, BuiltinProfiles...)
	for k := range RulesPresets {
		allowed = append(allowed, k)
	}
	return fmt.Errorf("unknown routing_profile %q (allowed: %s, or an http(s):// URL)", name, strings.Join(allowed, ", "))
}

// TestTimeout returns TestTimeoutMS as a duration.
func (c *Config) TestTimeout() time.Duration {
	return time.Duration(c.TestTimeoutMS) * time.Millisecond
}

// IsMihomo reports whether the native Mihomo backend is selected.
func (c *Config) IsMihomo() bool { return c.Core == "mihomo" }

// LocalPort is the SOCKS/mixed port exposed by the active backend.
func (c *Config) LocalPort() int { return c.XrayPort }

// MihomoControllerPort extracts the TCP port used for readiness checks.
func (c *Config) MihomoControllerPort() (int, error) {
	_, rawPort, err := net.SplitHostPort(c.MihomoController)
	if err != nil {
		return 0, fmt.Errorf("appconfig: mihomo_controller: %w", err)
	}
	var port int
	if _, err := fmt.Sscanf(rawPort, "%d", &port); err != nil || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("appconfig: invalid mihomo_controller port %q", rawPort)
	}
	return port, nil
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
		Core:                    "xray",
		SubscriptionIntervalSec: 3600,
		TestURL:                 "http://www.gstatic.com/generate_204",
		TestTimeoutMS:           3000,
		TestConcurrency:         5,
		XrayBin:                 "/usr/bin/xray",
		XrayConfig:              "~/.config/xray-waybar/xray.json",
		XrayPort:                1080,
		XrayAPIPort:             8080,
		MihomoBin:               "/usr/bin/mihomo",
		MihomoHome:              "~/.local/share/xray-waybar/mihomo",
		MihomoConfig:            "~/.local/share/xray-waybar/mihomo/config.yaml",
		MihomoSubscriptionFile:  "~/.cache/xray-waybar/mihomo-subscription.yaml",
		MihomoSecretFile:        "~/.local/share/xray-waybar/mihomo-secret",
		MihomoController:        "127.0.0.1:9090",
		MihomoTUNStack:          "mixed",
		PIDFile:                 "/tmp/xray-waybar.pid",
		LogFile:                 "~/.local/share/xray-waybar/xray.log",
		CacheFile:               "~/.cache/xray-waybar/servers.json",
		StateFile:               "~/.cache/xray-waybar/state.json",
		HWIDFile:                "~/.local/share/xray-waybar/hwid",
		RoutingProfile:          "proxy-all",
		Geo: GeoConfig{
			Dir: "~/.local/share/xray-waybar",
			// Stable "latest" redirects to the most recent dated release
			// asset. The GitHub repos publish .dat files via Releases,
			// not on the default branch.
			GeoipURL:         "https://github.com/hydraponique/roscomvpn-geoip/releases/latest/download/geoip.dat",
			GeositeURL:       "https://github.com/hydraponique/roscomvpn-geosite/releases/latest/download/geosite.dat",
			RefreshIntervalS: 86400,
		},
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

	c.Core = strings.ToLower(strings.TrimSpace(c.Core))
	if c.Core == "" {
		c.Core = "xray"
	}
	if c.Core != "xray" && c.Core != "mihomo" {
		return fmt.Errorf("appconfig: core must be xray or mihomo, got %q", c.Core)
	}

	var err error
	for _, p := range []*string{&c.XrayConfig, &c.PIDFile, &c.LogFile, &c.CacheFile, &c.StateFile, &c.HWIDFile, &c.XrayBin, &c.MihomoBin, &c.MihomoHome, &c.MihomoConfig, &c.MihomoSubscriptionFile, &c.MihomoSecretFile, &c.Geo.Dir} {
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
	if c.IsMihomo() {
		if _, err := c.MihomoControllerPort(); err != nil {
			return err
		}
		switch strings.ToLower(c.MihomoTUNStack) {
		case "system", "gvisor", "mixed":
			c.MihomoTUNStack = strings.ToLower(c.MihomoTUNStack)
		default:
			return fmt.Errorf("appconfig: mihomo_tun_stack must be system, gvisor or mixed")
		}
	}
	if c.RoutingProfile == "" {
		c.RoutingProfile = "proxy-all"
	}
	if err := ValidateProfile(c.RoutingProfile); err != nil {
		return fmt.Errorf("appconfig: %w", err)
	}
	if c.Geo.RefreshIntervalS < 0 {
		return errors.New("appconfig: geo.refresh_interval must be >= 0")
	}
	c.Geo.RefreshInterval = time.Duration(c.Geo.RefreshIntervalS) * time.Second
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
