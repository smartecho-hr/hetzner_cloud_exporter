// Package options builds the exporter's options from command line flags,
// environment variables and the config file.
package options

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/exporter-toolkit/web"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/config"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/sources"
)

// Options is the exporter configuration after applying flags, environment
// variables and the config file (see Parse).
type Options struct {
	ListenAddress   string
	MetricsPath     string
	HetznerAPIToken string
	TokenFile       string
	PollInterval    time.Duration
	MetricsInterval time.Duration
	Concurrency     int
	LogLevel        string
	ConfigFile      string
	WebConfigFile   string
	Version         bool

	// Collectors maps each collector name (e.g. "load_balancers") to whether it is enabled.
	Collectors map[string]bool
	// Intervals maps each collector name to how often it is fetched; 0 = every poll.
	Intervals map[string]time.Duration

	// ConfigLoaded is the config file that was read, "" if none.
	ConfigLoaded string
	// TokenSource says where the token came from, e.g. "HCLOUD_API_TOKEN_FILE
	// (/run/secrets/hcloud_token)", for the startup log. Never the token itself.
	TokenSource string

	// Warnings to log once the logger is set up.
	Warnings []string
}

// MinPollInterval is the shortest allowed --hcloud.poll-interval.
const MinPollInterval = 10 * time.Second

// MaxConcurrency is the highest allowed --hcloud.concurrency. More parallel
// requests don't help: the API budget is 3600 requests per hour.
const MaxConcurrency = 50

// ErrVersion is returned by Parse when --version was given.
var ErrVersion = errors.New("version requested")

// CollectorFlag returns the flag name of a collector: "load_balancers" -> "collector.load-balancers".
func CollectorFlag(name string) string {
	return "collector." + strings.ReplaceAll(name, "_", "-")
}

// collectorEnv returns the environment variable of a collector: "load_balancers" -> "COLLECTOR_LOAD_BALANCERS".
func collectorEnv(name string) string {
	return "COLLECTOR_" + strings.ToUpper(name)
}

func init() {
	for _, c := range sources.Collectors {
		flagEnv[CollectorFlag(c.Name)] = collectorEnv(c.Name)
		flagEnv[CollectorFlag(c.Name)+".interval"] = collectorEnv(c.Name) + "_INTERVAL"
	}
}

// flagEnv maps each flag to its environment variable.
// Precedence: command line flag > environment variable > config file > built-in default.
var flagEnv = map[string]string{
	"config.file":             "CONFIG_FILE",
	"hcloud.api-token":        "HCLOUD_API_TOKEN",
	"hcloud.api-token-file":   "HCLOUD_API_TOKEN_FILE",
	"hcloud.poll-interval":    "POLL_INTERVAL",
	"hcloud.metrics-interval": "METRICS_INTERVAL",
	"hcloud.concurrency":      "CONCURRENCY",
	"web.listen-address":      "LISTEN_ADDRESS",
	"web.metrics-path":        "METRICS_PATH",
	"web.config.file":         "WEB_CONFIG_FILE",
	"log.level":               "LOG_LEVEL",
}

// UnknownEnv returns a warning for every COLLECTOR_* environment variable
// that doesn't belong to a collector, e.g. a typo like COLLECTOR_SERVRS.
// The prefix is the exporter's own, so other variables are left alone.
// environ is usually os.Environ().
func UnknownEnv(environ []string) []string {
	known := make(map[string]bool)
	var names []string
	for _, c := range sources.Collectors {
		known[collectorEnv(c.Name)] = true
		known[collectorEnv(c.Name)+"_INTERVAL"] = true
		names = append(names, collectorEnv(c.Name))
	}
	var warnings []string
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "COLLECTOR_") && !known[name] {
			warnings = append(warnings, fmt.Sprintf("unknown environment variable %s is ignored; known: %s (each also with _INTERVAL)",
				name, strings.Join(names, ", ")))
		}
	}
	slices.Sort(warnings)
	return warnings
}

// reservedPaths are served by the exporter itself and can't be the metrics path.
var reservedPaths = []string{"/healthz", "/ready", "/version"}

type collectorFlag struct {
	name     string
	value    *bool
	interval *time.Duration
}

func newFlagSet(opts *Options, output io.Writer) (*flag.FlagSet, []collectorFlag) {
	var collectorFlags []collectorFlag
	fs := flag.NewFlagSet("hetzner_cloud_exporter", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // errors are returned; usage only on --help

	fs.StringVar(&opts.HetznerAPIToken, "hcloud.api-token", "", "Hetzner API token, read-only is enough (required, or --hcloud.api-token-file)")
	fs.StringVar(&opts.TokenFile, "hcloud.api-token-file", "", "File containing the Hetzner API token, e.g. a Docker or Kubernetes secret")
	opts.PollInterval, opts.MetricsInterval = 60*time.Second, 60*time.Second
	fs.Var(seconds{&opts.PollInterval}, "hcloud.poll-interval", "Interval between polls of the Hetzner API, e.g. 60s, 2m or 90 (seconds); API limit is 3600 requests/hour")
	fs.Var(seconds{&opts.MetricsInterval}, "hcloud.metrics-interval",
		"Interval for server and Load Balancer metrics (1 API request per resource); 0 turns them off. Lists are still polled every --hcloud.poll-interval")
	fs.IntVar(&opts.Concurrency, "hcloud.concurrency", 5, "Maximum number of concurrent Hetzner API requests")
	fs.StringVar(&opts.ListenAddress, "web.listen-address", fmt.Sprintf(":%d", config.DefaultPort), "Address on which to expose metrics and web interface")
	fs.StringVar(&opts.MetricsPath, "web.metrics-path", "/metrics", "Path under which to expose metrics")
	fs.StringVar(&opts.WebConfigFile, "web.config.file", "", "Prometheus web config file for TLS and/or basic auth (exporter-toolkit format), optional")
	fs.StringVar(&opts.LogLevel, "log.level", "info", "Log level: debug, info, warn, error")
	fs.StringVar(&opts.ConfigFile, "config.file", "config.yaml", "YAML config file; the default is read if it exists, a file set explicitly must exist")
	fs.BoolVar(&opts.Version, "version", false, "Show version and exit")

	opts.Collectors = make(map[string]bool)
	opts.Intervals = make(map[string]time.Duration)
	for _, c := range sources.Collectors {
		enabled := new(bool)
		fs.BoolVar(enabled, CollectorFlag(c.Name), c.Default, "Enable the collector for "+c.Help)
		interval := new(time.Duration)
		*interval = c.Interval
		fs.Var(seconds{interval}, CollectorFlag(c.Name)+".interval",
			"How often the collector is fetched from the API; 0 = every poll (--hcloud.poll-interval)")
		opts.Collectors[c.Name] = c.Default
		opts.Intervals[c.Name] = c.Interval
		collectorFlags = append(collectorFlags, collectorFlag{name: c.Name, value: enabled, interval: interval})
	}

	return fs, collectorFlags
}

// seconds is a duration flag that also accepts a bare number of seconds.
type seconds struct{ d *time.Duration }

func (s seconds) String() string {
	if s.d == nil {
		return ""
	}
	return formatDuration(*s.d)
}

func (s seconds) Set(value string) error {
	if n, err := strconv.Atoi(value); err == nil {
		if n > math.MaxInt64/int(time.Second) || n < math.MinInt64/int(time.Second) {
			return errors.New("too large")
		}
		*s.d = time.Duration(n) * time.Second
		return nil
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return errors.New("use a duration like 60s or 2m, or a number of seconds")
	}
	*s.d = d
	return nil
}

// formatDuration prints 60s as "1m", 10m as "10m", 1h as "1h" and 90s as "1m30s".
func formatDuration(d time.Duration) string {
	if d == 0 {
		return "0"
	}
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// printUsage prints the flags with their defaults and environment variables,
// using -- for long flags; the collector flags come last, in their own section.
func printUsage(fs *flag.FlagSet, w io.Writer) {
	fmt.Fprintf(w, "Usage of %s:\n", fs.Name())
	fmt.Fprintf(w, "  %s [flags]               run the exporter\n", fs.Name())
	fmt.Fprintf(w, "  %s healthcheck [flags]   exit 0 if the exporter accepts TCP connections on its listen address (for container health checks)\n\n", fs.Name())

	maxFlagLength := 0
	fs.VisitAll(func(f *flag.Flag) {
		maxFlagLength = max(maxFlagLength, len(f.Name))
	})

	line := func(f *flag.Flag) {
		usage := f.Usage
		if f.DefValue != "" && f.DefValue != "false" {
			usage += fmt.Sprintf(" (default %s)", f.DefValue)
		}
		if env, ok := flagEnv[f.Name]; ok {
			usage += fmt.Sprintf(" [env: %s]", env)
		}
		fmt.Fprintf(w, "  --%-*s  %s\n", maxFlagLength, f.Name, usage)
	}
	fs.VisitAll(func(f *flag.Flag) {
		if !strings.HasPrefix(f.Name, "collector.") {
			line(f)
		}
	})
	fmt.Fprintf(w, "\nCollectors (defaults shown per flag; --collector.<name>=false turns one off, --collector.<name>.interval sets how often it is fetched):\n")
	fs.VisitAll(func(f *flag.Flag) {
		if strings.HasPrefix(f.Name, "collector.") {
			line(f)
		}
	})
}

// Parse builds the options from args (without the program name), the
// environment (lookupEnv, usually os.LookupEnv) and the config file.
// It returns ErrVersion for --version and flag.ErrHelp for --help (usage is
// then already written to output).
func Parse(args []string, lookupEnv func(string) (string, bool), output io.Writer) (*Options, error) {
	opts, err := load(args, lookupEnv, output)
	if err != nil {
		return nil, err
	}
	if err := opts.validate(); err != nil {
		return nil, err
	}
	return opts, nil
}

// ListenAddress returns the listen address from args, the environment and
// the config file with the same precedence as Parse, but doesn't validate the
// other options (e.g. the token). Used by the healthcheck subcommand.
func ListenAddress(args []string, lookupEnv func(string) (string, bool)) (string, error) {
	opts, err := load(args, lookupEnv, io.Discard)
	if err != nil {
		return "", err
	}
	return opts.ListenAddress, nil
}

// load applies the command line, the environment and the config file.
func load(args []string, lookupEnv func(string) (string, bool), output io.Writer) (*Options, error) {
	opts := &Options{}
	fs, collectorFlags := newFlagSet(opts, output)

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printUsage(fs, output)
		}
		// Env vars and config keys use underscores, flags use dashes.
		if name, ok := strings.CutPrefix(err.Error(), "flag provided but not defined: -"); ok && strings.Contains(name, "_") {
			name = strings.TrimPrefix(name, "-")
			return nil, fmt.Errorf("%w (flags use dashes: --%s)", err, strings.ReplaceAll(name, "_", "-"))
		}
		return nil, err
	}
	if opts.Version {
		return nil, ErrVersion
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}

	// Remember what the command line set; lower layers must not override it.
	// An empty value (e.g. --hcloud.api-token=$UNSET_VAR) counts as unset,
	// like an empty environment variable.
	set := make(map[string]layer)
	fs.Visit(func(f *flag.Flag) {
		if f.Value.String() == "" {
			_ = f.Value.Set(f.DefValue)
			return
		}
		set[f.Name] = fromFlag
	})

	// Environment variables fill in what the command line didn't set.
	for _, name := range slices.Sorted(maps.Keys(flagEnv)) {
		env := flagEnv[name]
		if set[name] != unset {
			continue
		}
		if value, ok := lookupEnv(env); ok && value != "" {
			if err := fs.Set(name, value); err != nil {
				return nil, fmt.Errorf("invalid value %q for %s: %w", value, env, err)
			}
			set[name] = fromEnv
		}
	}

	// The config file fills in what neither the command line nor the
	// environment set. It is optional, unless a path was given explicitly.
	cfg, err := config.Load(opts.ConfigFile, set["config.file"] != unset)
	if err != nil {
		return nil, err
	}
	if cfg != nil {
		opts.ConfigLoaded = opts.ConfigFile
		values := cfg.FlagValues()
		for _, name := range slices.Sorted(maps.Keys(values)) {
			if set[name] != unset {
				continue
			}
			if strings.HasPrefix(name, "collector.") && fs.Lookup(name) == nil {
				collector, section := strings.TrimPrefix(name, "collector."), "collectors"
				if trimmed, ok := strings.CutSuffix(collector, ".interval"); ok {
					collector, section = trimmed, "collector_intervals"
				}
				opts.Warnings = append(opts.Warnings, fmt.Sprintf(
					"config file %s: unknown collector %q in %s, ignored", opts.ConfigFile, collector, section))
				continue
			}
			if err := fs.Set(name, values[name]); err != nil {
				// The value isn't repeated: a token pasted under the wrong key must not end up in logs.
				return nil, fmt.Errorf("invalid value for %s in %s: %w", name, opts.ConfigFile, err)
			}
			set[name] = fromConfig
		}
		opts.Warnings = append(opts.Warnings, cfg.Warnings()...)
	}

	if err := opts.chooseToken(set); err != nil {
		return nil, err
	}

	for _, c := range collectorFlags {
		opts.Collectors[c.name] = *c.value
		opts.Intervals[c.name] = *c.interval
	}
	return opts, nil
}

// layer is where an option was set; a higher layer wins.
type layer int

const (
	unset layer = iota
	fromConfig
	fromEnv
	fromFlag
)

// configKeys names the config file keys of the token options, for messages.
var configKeys = map[string]string{"hcloud.api-token": "api.token", "hcloud.api-token-file": "api.token_file"}

// describe returns how the option was set, e.g. "HCLOUD_API_TOKEN".
func (opts *Options) describe(name string, l layer) string {
	switch l {
	case fromFlag:
		return "--" + name
	case fromEnv:
		return flagEnv[name]
	default:
		return configKeys[name] + " in " + opts.ConfigFile
	}
}

// chooseToken treats the token and the token file as one option: the one
// from the higher layer wins (flag > env > config file), so e.g.
// HCLOUD_API_TOKEN_FILE overrides api.token in the config file. Both in the
// same layer is an error. The file itself is read in validate.
func (opts *Options) chooseToken(set map[string]layer) error {
	const tokenName, fileName = "hcloud.api-token", "hcloud.api-token-file"
	token, file := set[tokenName], set[fileName]
	switch {
	case token == unset && file == unset:
	case token == file:
		return fmt.Errorf("both %s and %s are set; use only one", opts.describe(tokenName, token), opts.describe(fileName, file))
	case file > token:
		if token != unset {
			opts.Warnings = append(opts.Warnings, fmt.Sprintf("%s is ignored: %s takes precedence",
				opts.describe(tokenName, token), opts.describe(fileName, file)))
		}
		opts.HetznerAPIToken = ""
		opts.TokenSource = fmt.Sprintf("%s (%s)", opts.describe(fileName, file), opts.TokenFile)
	default:
		if file != unset {
			opts.Warnings = append(opts.Warnings, fmt.Sprintf("%s (%s) is ignored: %s takes precedence",
				opts.describe(fileName, file), opts.TokenFile, opts.describe(tokenName, token)))
		}
		opts.TokenFile = ""
		opts.TokenSource = opts.describe(tokenName, token)
	}
	return nil
}

func (opts *Options) validate() error {
	if opts.TokenFile != "" {
		data, err := os.ReadFile(opts.TokenFile)
		if err != nil {
			if errors.Is(err, os.ErrPermission) && runtime.GOOS != "windows" { // no UIDs on Windows
				return fmt.Errorf("reading the token file: %w (the exporter runs as UID %d)", err, os.Getuid())
			}
			return fmt.Errorf("reading the token file: %w", err)
		}
		opts.HetznerAPIToken = string(data)
	}

	// Tokens copied from files or secrets often end with a newline.
	opts.HetznerAPIToken = strings.TrimSpace(opts.HetznerAPIToken)
	if opts.HetznerAPIToken == "" && opts.TokenFile != "" {
		return fmt.Errorf("the token file %s is empty", opts.TokenFile)
	}
	if opts.HetznerAPIToken == "" {
		return fmt.Errorf("the Hetzner API token is required: set HCLOUD_API_TOKEN, --hcloud.api-token, --hcloud.api-token-file or api.token in %s", opts.ConfigFile)
	}
	if isPlaceholder(opts.HetznerAPIToken) {
		return errors.New("the Hetzner API token is still the placeholder from the example config; " +
			"create a read-only token in the Hetzner Console (Security -> API tokens) and set it")
	}
	if strings.ContainsFunc(opts.HetznerAPIToken, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return errors.New("the Hetzner API token contains whitespace or control characters")
	}

	if opts.PollInterval < MinPollInterval {
		return fmt.Errorf("--hcloud.poll-interval must be at least %s, got %s", MinPollInterval, opts.PollInterval)
	}
	if !slices.Contains(slices.Collect(maps.Values(opts.Collectors)), true) {
		return errors.New("all collectors are disabled, enable at least one --collector.*")
	}
	if opts.MetricsInterval != 0 && opts.MetricsInterval < MinPollInterval {
		return fmt.Errorf("--hcloud.metrics-interval must be 0 (off) or at least %s, got %s", MinPollInterval, opts.MetricsInterval)
	}
	if opts.MetricsInterval != 0 && opts.MetricsInterval < time.Minute {
		opts.Warnings = append(opts.Warnings, fmt.Sprintf(
			"--hcloud.metrics-interval %s: Hetzner's metrics have a 60s resolution, a shorter interval only costs API requests",
			formatDuration(opts.MetricsInterval)))
	}
	for _, name := range slices.Sorted(maps.Keys(opts.Intervals)) {
		if interval := opts.Intervals[name]; interval != 0 && interval < MinPollInterval {
			return fmt.Errorf("--%s.interval must be 0 (every poll) or at least %s, got %s", CollectorFlag(name), MinPollInterval, interval)
		}
	}
	if opts.Concurrency < 1 || opts.Concurrency > MaxConcurrency {
		return fmt.Errorf("--hcloud.concurrency must be between 1 and %d, got %d", MaxConcurrency, opts.Concurrency)
	}
	if !slices.Contains([]string{"debug", "info", "warn", "warning", "error"}, strings.ToLower(opts.LogLevel)) {
		return fmt.Errorf("--log.level must be one of debug, info, warn, error, got %q", opts.LogLevel)
	}

	if err := web.Validate(opts.WebConfigFile); err != nil {
		return fmt.Errorf("--web.config.file %s: %w", opts.WebConfigFile, err)
	}

	path := opts.MetricsPath
	switch {
	case !strings.HasPrefix(path, "/"):
		return fmt.Errorf("--web.metrics-path must start with /, got %q", path)
	case strings.ContainsFunc(path, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return fmt.Errorf("--web.metrics-path must not contain control characters, got %q", path)
	case strings.ContainsAny(path, " \t{}"):
		return fmt.Errorf("--web.metrics-path must not contain spaces or braces, got %q", path)
	case strings.ContainsAny(path, "?#%"):
		return fmt.Errorf("--web.metrics-path must be a plain path without ?, # or %%, got %q", path)
	case strings.Contains(path, "//") || slices.Contains(strings.Split(path, "/"), "..") || slices.Contains(strings.Split(path, "/"), "."):
		return fmt.Errorf("--web.metrics-path must not contain //, . or .. segments, got %q", path)
	case slices.Contains(reservedPaths, path):
		return fmt.Errorf("--web.metrics-path %q is reserved for another endpoint", path)
	}
	return nil
}

// isPlaceholder reports whether the token is an example value from
// config.example.yaml or docker/.env.example.
func isPlaceholder(token string) bool {
	lower := strings.ToLower(token)
	return strings.Contains(lower, "<") || strings.Contains(lower, "your_token") ||
		strings.Contains(lower, "your-hcloud-api-token") || strings.Contains(lower, "your-token") ||
		strings.Contains(lower, "your-read-only-token")
}
