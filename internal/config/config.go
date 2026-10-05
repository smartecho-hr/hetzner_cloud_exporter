// Package config reads the optional YAML config file (config.yaml).
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Config is the YAML configuration file.
// Every field is optional; command line flags and environment variables take
// precedence over values from the file.
type Config struct {
	Server struct {
		Host          string `yaml:"host"`
		Port          int    `yaml:"port"`
		MetricsPath   string `yaml:"metrics_path"`
		WebConfigFile string `yaml:"web_config_file"`
	} `yaml:"server"`
	API struct {
		Token           string `yaml:"token"`
		TokenFile       string `yaml:"token_file"`
		PollInterval    string `yaml:"poll_interval"`
		MetricsInterval string `yaml:"metrics_interval"`
		Concurrency     int    `yaml:"concurrency"`
	} `yaml:"api"`
	Log struct {
		Level string `yaml:"level"`
	} `yaml:"log"`

	// Collectors turns sources on or off, e.g. {"certificates": false}.
	Collectors map[string]bool `yaml:"collectors"`

	// CollectorIntervals sets how often a source is fetched, e.g.
	// {"images": "30m"}; a bare number means seconds, 0 = every poll.
	CollectorIntervals map[string]string `yaml:"collector_intervals"`

	warnings []string
}

// DefaultPort is used when the file sets server.host but no server.port.
const DefaultPort = 9210

// Load reads the config file at path. A missing file returns (nil, nil)
// unless required is true.
func Load(path string, required bool) (*Config, error) {
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist) && !required:
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	cfg := &Config{}
	if err := cfg.parse(path, data); err != nil {
		return nil, err
	}
	cfg.checkPermissions(path)
	return cfg, nil
}

func (c *Config) parse(path string, data []byte) error {
	if err := yaml.Unmarshal(data, c); err != nil {
		return fmt.Errorf("parsing config file %s: %s", path, strings.Join(yamlProblems(err), "; "))
	}

	// Sorted, so the same file always reports the same pair.
	for _, section := range []struct {
		name  string
		names []string
	}{
		{"collectors", slices.Sorted(maps.Keys(c.Collectors))},
		{"collector_intervals", slices.Sorted(maps.Keys(c.CollectorIntervals))},
	} {
		section, names := section.name, section.names
		seen := make(map[string]string)
		for _, name := range names {
			flag := collectorFlag(name)
			if other, dup := seen[flag]; dup {
				return fmt.Errorf("config file %s: %s %q and %q are the same collector", path, section, other, name)
			}
			seen[flag] = name
		}
	}

	// Decode again strictly to find unknown (e.g. misspelled) keys. They are
	// reported as warnings so that older files keep working.
	strict := yaml.NewDecoder(bytes.NewReader(data))
	strict.KnownFields(true)
	var ignored Config
	if err := strict.Decode(&ignored); err != nil && !errors.Is(err, io.EOF) { // io.EOF: empty file
		for _, problem := range yamlProblems(err) {
			c.warnings = append(c.warnings, fmt.Sprintf("config file %s: %s", path, problem))
		}
	}
	return nil
}

func (c *Config) checkPermissions(path string) {
	// Windows has no Unix file modes: every file would look readable by others.
	if c.API.Token == "" || runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err == nil && info.Mode().Perm()&0o077 != 0 {
		c.warnings = append(c.warnings, fmt.Sprintf(
			"config file %s contains the API token but is readable by other users (mode %v); run chmod 600 %s",
			path, info.Mode().Perm(), path))
	}
}

// FlagValues returns the configured values keyed by command line flag name.
// Options that are not set are left out.
func (c *Config) FlagValues() map[string]string {
	values := make(map[string]string)
	set := func(flag, value string) {
		if value != "" {
			values[flag] = value
		}
	}

	if c.Server.Host != "" || c.Server.Port != 0 {
		port := c.Server.Port
		if port == 0 {
			port = DefaultPort
		}
		host := strings.Trim(c.Server.Host, "[]") // "[::1]" -> "::1", JoinHostPort adds brackets
		values["web.listen-address"] = net.JoinHostPort(host, strconv.Itoa(port))
	}
	set("web.metrics-path", c.Server.MetricsPath)
	set("web.config.file", c.Server.WebConfigFile)
	set("hcloud.api-token", c.API.Token)
	set("hcloud.api-token-file", c.API.TokenFile)
	set("hcloud.poll-interval", withSecondsUnit(c.API.PollInterval))
	set("hcloud.metrics-interval", withSecondsUnit(c.API.MetricsInterval))
	if c.API.Concurrency != 0 {
		values["hcloud.concurrency"] = strconv.Itoa(c.API.Concurrency)
	}
	set("log.level", c.Log.Level)
	for name, enabled := range c.Collectors {
		values[collectorFlag(name)] = strconv.FormatBool(enabled)
	}
	for name, interval := range c.CollectorIntervals {
		values[collectorFlag(name)+".interval"] = withSecondsUnit(interval)
	}
	return values
}

var (
	unknownKey = regexp.MustCompile(`^(line \d+): field (\S+) not found in type .*$`)
	wrongType  = regexp.MustCompile(`^(line \d+): cannot unmarshal (!!\w+) .* into .*$`)
	valueQuote = regexp.MustCompile("`[^`]*`")
)

// yamlProblems turns a YAML error into one readable message per problem,
// e.g. `line 3: unknown key "tokn"`. Values are never repeated, so a token
// in the wrong place doesn't end up in logs.
func yamlProblems(err error) []string {
	var lines []string
	var typeErr *yaml.TypeError
	if errors.As(err, &typeErr) {
		lines = typeErr.Errors
	} else {
		lines = []string{err.Error()}
	}
	for i, line := range lines {
		line = strings.TrimPrefix(line, "yaml: ")
		if m := unknownKey.FindStringSubmatch(line); m != nil {
			line = fmt.Sprintf("%s: unknown key %q", m[1], m[2])
		}
		if m := wrongType.FindStringSubmatch(line); m != nil {
			line = fmt.Sprintf("%s: wrong type (%s) for this key", m[1], m[2])
		}
		lines[i] = valueQuote.ReplaceAllString(line, "the value")
	}
	return lines
}

// collectorFlag returns the flag of a collector key; "load_balancers" and
// "load-balancers" both become "collector.load-balancers".
func collectorFlag(name string) string {
	return "collector." + strings.NewReplacer("_", "-", ".", "-").Replace(name)
}

// withSecondsUnit turns a bare number ("60") into seconds ("60s").
func withSecondsUnit(interval string) string {
	if _, err := strconv.Atoi(interval); err == nil {
		return interval + "s"
	}
	return interval
}

// Warnings returns notes about options that are ignored or risky.
func (c *Config) Warnings() []string {
	return append([]string(nil), c.warnings...)
}
