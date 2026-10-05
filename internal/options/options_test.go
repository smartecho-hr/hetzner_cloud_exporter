package options

import (
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/config"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/sources"
)

func env(vars map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := vars[key]
		return value, ok
	}
}

// configFile writes a config file and returns the --config.file argument.
func configFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPrecedence(t *testing.T) {
	file := configFile(t, `
server:
  port: 9301
api:
  token: file-token
  poll_interval: 3m
  concurrency: 3
log:
  level: warn
`)

	tests := []struct {
		name string
		args []string
		env  map[string]string
		want Options
	}{
		{
			name: "defaults with token from env",
			env:  map[string]string{"HCLOUD_API_TOKEN": "env-token"},
			want: Options{HetznerAPIToken: "env-token", ListenAddress: ":9210", MetricsPath: "/metrics",
				PollInterval: time.Minute, Concurrency: 5, LogLevel: "info"},
		},
		{
			name: "config file fills in",
			args: []string{"--config.file", file},
			want: Options{HetznerAPIToken: "file-token", ListenAddress: ":9301", MetricsPath: "/metrics",
				PollInterval: 3 * time.Minute, Concurrency: 3, LogLevel: "warn"},
		},
		{
			name: "env over config file",
			args: []string{"--config.file", file},
			env:  map[string]string{"HCLOUD_API_TOKEN": "env-token", "POLL_INTERVAL": "2m", "LISTEN_ADDRESS": ":9302"},
			want: Options{HetznerAPIToken: "env-token", ListenAddress: ":9302", MetricsPath: "/metrics",
				PollInterval: 2 * time.Minute, Concurrency: 3, LogLevel: "warn"},
		},
		{
			name: "flag over env over config file",
			args: []string{"--config.file", file, "--hcloud.poll-interval", "90s", "--log.level=debug"},
			env:  map[string]string{"POLL_INTERVAL": "2m", "LOG_LEVEL": "error"},
			want: Options{HetznerAPIToken: "file-token", ListenAddress: ":9301", MetricsPath: "/metrics",
				PollInterval: 90 * time.Second, Concurrency: 3, LogLevel: "debug"},
		},
		{
			name: "empty env var counts as unset",
			args: []string{"--config.file", file},
			env:  map[string]string{"HCLOUD_API_TOKEN": "", "POLL_INTERVAL": ""},
			want: Options{HetznerAPIToken: "file-token", ListenAddress: ":9301", MetricsPath: "/metrics",
				PollInterval: 3 * time.Minute, Concurrency: 3, LogLevel: "warn"},
		},
		{
			name: "token is trimmed",
			env:  map[string]string{"HCLOUD_API_TOKEN": " env-token\n"},
			want: Options{HetznerAPIToken: "env-token", ListenAddress: ":9210", MetricsPath: "/metrics",
				PollInterval: time.Minute, Concurrency: 5, LogLevel: "info"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vars := map[string]string{}
			if !containsArg(tt.args, "--config.file") {
				t.Chdir(t.TempDir()) // no config.yaml in the working directory
			}
			for k, v := range tt.env {
				vars[k] = v
			}

			opts, err := Parse(tt.args, env(vars), io.Discard)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			got := *opts
			got.ConfigFile, got.ConfigLoaded, got.Warnings, got.Collectors = "", "", nil, nil // collectors: see TestCollectors
			got.Intervals = nil                                                               // see TestCollectorIntervals
			got.MetricsInterval = 0                                                           // see TestMetricsInterval
			got.TokenSource = ""                                                              // see TestTokenAndTokenFile
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Options =\n  %+v\nwant\n  %+v", got, tt.want)
			}
		})
	}
}

func containsArg(args []string, name string) bool {
	for _, arg := range args {
		if arg == name || strings.HasPrefix(arg, name+"=") {
			return true
		}
	}
	return false
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		env     map[string]string
		wantErr string
	}{
		{"no token", nil, nil, "token is required"},
		{"token with inner whitespace", nil, map[string]string{"HCLOUD_API_TOKEN": "ab cd"}, "whitespace"},
		{"poll interval too short", []string{"--hcloud.poll-interval=5s"}, nil, "at least 10s"},
		{"concurrency zero", []string{"--hcloud.concurrency=0"}, nil, "between 1 and 50"},
		{"concurrency too high", []string{"--hcloud.concurrency=500"}, nil, "between 1 and 50"},
		{"invalid log level", []string{"--log.level=verbose"}, nil, "--log.level"},
		{"undocumented log level", []string{"--log.level=panic"}, nil, "--log.level"},
		{"duration overflow", []string{"--hcloud.poll-interval=9300000000"}, nil, "too large"},
		{"collector flag with underscore", []string{"--collector.load_balancers=false"}, nil, "--collector.load-balancers"},
		{"metrics path with control character", []string{"--web.metrics-path=/a\nb"}, nil, "control characters"},
		{"README placeholder token", nil, map[string]string{"HCLOUD_API_TOKEN": "your-read-only-token"}, "placeholder"},
		{"metrics path with query", []string{"--web.metrics-path=/metrics?x=1"}, nil, "plain path"},
		{"metrics path with fragment", []string{"--web.metrics-path=/metrics#x"}, nil, "plain path"},
		{"metrics path with double slash", []string{"--web.metrics-path=//metrics"}, nil, "//"},
		{"metrics path with dot dot", []string{"--web.metrics-path=/a/../metrics"}, nil, ".."},
		{"metrics path without slash", []string{"--web.metrics-path=metrics"}, nil, "must start with /"},
		{"metrics path reserved", []string{"--web.metrics-path=/healthz"}, nil, "reserved"},
		{"metrics path pattern", []string{"--web.metrics-path=/{x}"}, nil, "braces"},
		{"invalid env value", nil, map[string]string{"POLL_INTERVAL": "abc"}, "POLL_INTERVAL"},
		{"unexpected argument", []string{"extra"}, nil, "unexpected arguments"},
		{"explicit missing config file", []string{"--config.file=/nonexistent.yaml"}, nil, "reading config file"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			vars := map[string]string{"HCLOUD_API_TOKEN": "token"}
			if tt.name == "no token" {
				delete(vars, "HCLOUD_API_TOKEN")
			}
			for k, v := range tt.env {
				vars[k] = v
			}

			_, err := Parse(tt.args, env(vars), io.Discard)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestVersionAndHelp(t *testing.T) {
	// --version works even with an invalid environment and no token.
	_, err := Parse([]string{"--version"}, env(map[string]string{"POLL_INTERVAL": "abc"}), io.Discard)
	if !errors.Is(err, ErrVersion) {
		t.Errorf("--version: err = %v, want ErrVersion", err)
	}

	var out strings.Builder
	_, err = Parse([]string{"--help"}, env(nil), &out)
	if !errors.Is(err, flag.ErrHelp) {
		t.Errorf("--help: err = %v, want flag.ErrHelp", err)
	}
	for _, want := range []string{"--hcloud.api-token", "[env: HCLOUD_API_TOKEN]", "(default 1m)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("usage does not contain %q:\n%s", want, out.String())
		}
	}
}

func TestCollectors(t *testing.T) {
	file := configFile(t, `
api:
  token: t
collectors:
  certificates: false
  load_balancers: false
  volumez: true
`)

	tests := []struct {
		name string
		args []string
		env  map[string]string
		want map[string]bool
	}{
		{"defaults", nil, map[string]string{"HCLOUD_API_TOKEN": "t"},
			map[string]bool{"servers": true, "load_balancers": true, "certificates": true}},
		{"config file", []string{"--config.file", file}, nil,
			map[string]bool{"servers": true, "load_balancers": false, "certificates": false}},
		{"env over config file", []string{"--config.file", file}, map[string]string{"COLLECTOR_LOAD_BALANCERS": "true"},
			map[string]bool{"servers": true, "load_balancers": true, "certificates": false}},
		{"flag over env", []string{"--config.file", file, "--collector.load-balancers=false"}, map[string]string{"COLLECTOR_LOAD_BALANCERS": "true"},
			map[string]bool{"servers": true, "load_balancers": false, "certificates": false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			opts, err := Parse(tt.args, env(tt.env), io.Discard)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			for name, want := range tt.want {
				if opts.Collectors[name] != want {
					t.Errorf("collector %s = %v, want %v", name, opts.Collectors[name], want)
				}
			}
		})
	}

	// Unknown collector names in the config file are reported.
	t.Chdir(t.TempDir())
	opts, err := Parse([]string{"--config.file", file}, env(nil), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(opts.Warnings, "\n"), `unknown collector "volumez"`) {
		t.Errorf("warnings = %v, want one about volumez", opts.Warnings)
	}

	// At least one collector must be enabled.
	var allOff []string
	for _, c := range sources.Collectors {
		allOff = append(allOff, "--"+CollectorFlag(c.Name)+"=false")
	}
	_, err = Parse(allOff, env(map[string]string{"HCLOUD_API_TOKEN": "t"}), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "all collectors are disabled") {
		t.Errorf("all disabled: err = %v", err)
	}
}

func TestWebConfigFile(t *testing.T) {
	t.Chdir(t.TempDir())
	vars := map[string]string{"HCLOUD_API_TOKEN": "t"}

	// Basic auth only (no TLS) is a valid web config.
	valid := filepath.Join(t.TempDir(), "web.yml")
	if err := os.WriteFile(valid, []byte("basic_auth_users:\n  prometheus: $2a$10$abcdefghijklmnopqrstuuwz6Xbq9W8iFhH4jUW5o8mYk1N0wQXbu\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts, err := Parse([]string{"--web.config.file", valid}, env(vars), io.Discard)
	if err != nil || opts.WebConfigFile != valid {
		t.Fatalf("valid web config: opts=%v err=%v", opts, err)
	}

	invalid := filepath.Join(t.TempDir(), "bad.yml")
	if err := os.WriteFile(invalid, []byte("tls_server_config:\n  cert_file: /nonexistent.pem\n  key_file: /nonexistent.pem\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse([]string{"--web.config.file", invalid}, env(vars), io.Discard); err == nil || !strings.Contains(err.Error(), "web.config.file") {
		t.Errorf("invalid web config: err = %v", err)
	}
}

func TestMetricsInterval(t *testing.T) {
	t.Chdir(t.TempDir())
	token := map[string]string{"HCLOUD_API_TOKEN": "t"}

	tests := []struct {
		name string
		args []string
		env  map[string]string
		want time.Duration
	}{
		{"default", nil, nil, time.Minute},
		{"flag", []string{"--hcloud.metrics-interval=2m"}, nil, 2 * time.Minute},
		{"off", []string{"--hcloud.metrics-interval=0"}, nil, 0},
		{"env", nil, map[string]string{"METRICS_INTERVAL": "5m"}, 5 * time.Minute},
		{"config file, 0 means off", []string{"--config.file", configFile(t, "api:\n  token: t\n  metrics_interval: 0\n")}, nil, 0},
		{"config file, bare seconds", []string{"--config.file", configFile(t, "api:\n  token: t\n  metrics_interval: 120\n")}, nil, 2 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vars := map[string]string{}
			for k, v := range token {
				vars[k] = v
			}
			for k, v := range tt.env {
				vars[k] = v
			}
			opts, err := Parse(tt.args, env(vars), io.Discard)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if opts.MetricsInterval != tt.want {
				t.Errorf("MetricsInterval = %v, want %v", opts.MetricsInterval, tt.want)
			}
		})
	}

	if _, err := Parse([]string{"--hcloud.metrics-interval=5s"}, env(token), io.Discard); err == nil ||
		!strings.Contains(err.Error(), "0 (off) or at least") {
		t.Errorf("5s: err = %v, want error", err)
	}

	// Below Hetzner's 60s resolution: allowed, with a warning.
	opts, err := Parse([]string{"--hcloud.metrics-interval=30s"}, env(token), io.Discard)
	if err != nil || !strings.Contains(strings.Join(opts.Warnings, "\n"), "60s resolution") {
		t.Errorf("30s: err = %v, warnings = %v, want a resolution warning", err, opts.Warnings)
	}
}

// Invalid values in the config file name the option and the file.
func TestInvalidConfigValues(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, tt := range []struct{ yaml, want string }{
		{"api:\n  token: t\n  poll_interval: soon\n", "hcloud.poll-interval"},
		{"api:\n  token: t\n  concurrency: -1\n", "concurrency"},
		{"log:\n  level: loud\napi:\n  token: t\n", "--log.level"},
		{"collector_intervals:\n  images: often\napi:\n  token: t\n", "collector.images.interval"},
	} {
		_, err := Parse([]string{"--config.file", configFile(t, tt.yaml)}, env(nil), io.Discard)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%q: err = %v, want containing %q", tt.yaml, err, tt.want)
		}
	}
}

// An empty --hcloud.api-token= (e.g. from an unset shell variable) counts as
// unset, so the token file from the environment is used.
func TestEmptyFlagCountsAsUnset(t *testing.T) {
	t.Chdir(t.TempDir())
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("file-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts, err := Parse([]string{"--hcloud.api-token=", "--web.listen-address="},
		env(map[string]string{"HCLOUD_API_TOKEN_FILE": file}), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if opts.HetznerAPIToken != "file-secret" || opts.ListenAddress != ":9210" {
		t.Errorf("token %q, listen address %q: want the file's token and the default address", opts.HetznerAPIToken, opts.ListenAddress)
	}
}

// Invalid values in the config file are not echoed (a token under the wrong key).
func TestConfigValueNotEchoed(t *testing.T) {
	t.Chdir(t.TempDir())
	_, err := Parse([]string{"--config.file", configFile(t, "api:\n  token: t\n  poll_interval: SECRETVALUE\n")}, env(nil), io.Discard)
	if err == nil || strings.Contains(err.Error(), "SECRETVALUE") {
		t.Errorf("err = %v, want an error without the value", err)
	}
}

// A token file the exporter can't read stops it with the reason.
func TestTokenFilePermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("needs Unix permissions and a non-root user")
	}
	t.Chdir(t.TempDir())
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("secret"), 0o000); err != nil {
		t.Fatal(err)
	}
	_, err := Parse([]string{"--hcloud.api-token-file", file}, env(nil), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "permission denied") || !strings.Contains(err.Error(), "UID") {
		t.Errorf("err = %v, want permission denied with the UID", err)
	}
}

// TestEveryOptionInEveryPlace makes sure each option can be set as a flag,
// an environment variable and in the config file, and is shown in the
// example files. A new option has to be added in all of them.
func TestEveryOptionInEveryPlace(t *testing.T) {
	// A config file with every key; add a new option's key here too.
	full := `
server:
  host: 127.0.0.1
  port: 9999
  metrics_path: /m
  web_config_file: web.yml
api:
  token: t
  token_file: f
  poll_interval: 30s
  metrics_interval: 30s
  concurrency: 3
log:
  level: debug
collectors:
`
	for _, c := range sources.Collectors {
		full += "  " + c.Name + ": false\n"
	}
	full += "collector_intervals:\n"
	for _, c := range sources.Collectors {
		full += "  " + c.Name + ": 30m\n"
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(full), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if w := cfg.Warnings(); len(w) > 0 {
		t.Errorf("full config file has warnings: %v", w)
	}
	inFile := cfg.FlagValues()

	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	exampleYAML, envExample, usage := read("config.example.yaml"), read("docker/.env.example"), read("README.md")

	var opts Options
	fs, _ := newFlagSet(&opts, io.Discard)
	fs.VisitAll(func(f *flag.Flag) {
		if f.Name == "version" {
			return
		}
		env, ok := flagEnv[f.Name]
		if !ok {
			t.Errorf("--%s has no environment variable (flagEnv)", f.Name)
		}
		collector := strings.HasPrefix(f.Name, "collector.")
		if f.Name != "config.file" { // the file can't name itself
			if _, ok := inFile[f.Name]; !ok {
				t.Errorf("--%s has no config file key (config.Config / FlagValues), or the key is missing in this test", f.Name)
			}
		}
		if !collector && !strings.Contains(usage, "`--"+f.Name+"`") {
			t.Errorf("--%s is not documented in README.md", f.Name)
		}
		if ok && !collector && !strings.Contains(envExample, env+"=") {
			t.Errorf("%s is missing in docker/.env.example", env)
		}
	})
	for _, line := range strings.Split(strings.TrimSpace(full), "\n") {
		key := strings.TrimSpace(strings.SplitN(line, ":", 2)[0])
		if !strings.Contains(exampleYAML, key+":") {
			t.Errorf("config key %q is missing in config.example.yaml", key)
		}
	}
	if !strings.Contains(envExample, "COLLECTOR_") {
		t.Error("docker/.env.example shows no COLLECTOR_* example")
	}
}

func TestUnknownEnv(t *testing.T) {
	got := UnknownEnv([]string{"COLLECTOR_SERVERS=false", "COLLECTOR_SERVRS=false", "PATH=/bin", "HCLOUD_TOKEN=x", "COLLECTOR_=1"})
	if len(got) != 2 || !strings.Contains(got[0], "COLLECTOR_ ") || !strings.Contains(got[1], "COLLECTOR_SERVRS") ||
		!strings.Contains(got[1], "COLLECTOR_SERVERS") {
		t.Errorf("UnknownEnv = %q, want warnings for COLLECTOR_ and COLLECTOR_SERVRS only", got)
	}
}

func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0: "0", 30 * time.Second: "30s", time.Minute: "1m", 90 * time.Second: "1m30s",
		10 * time.Minute: "10m", time.Hour: "1h", 70 * time.Minute: "1h10m", 6 * time.Hour: "6h",
	} {
		if got := formatDuration(d); got != want {
			t.Errorf("formatDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestCollectorIntervals(t *testing.T) {
	t.Chdir(t.TempDir())
	file := configFile(t, "api:\n  token: t\ncollector_intervals:\n  images: 30m\n  ssh_keys: 0\n  pricing: 7200\n  volumez: 1h\n")

	tests := []struct {
		name string
		args []string
		env  map[string]string
		want map[string]time.Duration
	}{
		{"defaults", nil, map[string]string{"HCLOUD_API_TOKEN": "t"},
			map[string]time.Duration{"pricing": time.Hour, "images": 10 * time.Minute, "storage_boxes": 5 * time.Minute,
				"ssh_keys": time.Hour, "servers": 0}},
		{"config file, bare seconds, 0 = every poll", []string{"--config.file", file}, nil,
			map[string]time.Duration{"images": 30 * time.Minute, "ssh_keys": 0, "pricing": 2 * time.Hour, "storage_boxes": 5 * time.Minute}},
		{"env over config file", []string{"--config.file", file}, map[string]string{"COLLECTOR_IMAGES_INTERVAL": "1h"},
			map[string]time.Duration{"images": time.Hour}},
		{"flag over env", []string{"--config.file", file, "--collector.images.interval=2h"}, map[string]string{"COLLECTOR_IMAGES_INTERVAL": "1h"},
			map[string]time.Duration{"images": 2 * time.Hour}},
		{"flag for a collector polled every poll", []string{"--collector.load-balancers.interval=5m"}, map[string]string{"HCLOUD_API_TOKEN": "t"},
			map[string]time.Duration{"load_balancers": 5 * time.Minute}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, err := Parse(tt.args, env(tt.env), io.Discard)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			for name, want := range tt.want {
				if got := opts.Intervals[name]; got != want {
					t.Errorf("interval %s = %v, want %v", name, got, want)
				}
			}
		})
	}

	// Unknown collectors in collector_intervals are reported.
	opts, err := Parse([]string{"--config.file", file}, env(nil), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(opts.Warnings, "\n"), `unknown collector "volumez" in collector_intervals`) {
		t.Errorf("warnings = %v, want one about volumez", opts.Warnings)
	}

	// Below 10s (other than 0) is rejected.
	_, err = Parse([]string{"--collector.pricing.interval=5s"}, env(map[string]string{"HCLOUD_API_TOKEN": "t"}), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--collector.pricing.interval must be 0 (every poll) or at least 10s") {
		t.Errorf("5s: err = %v", err)
	}

	// The interval variables are known, not reported as typos.
	if got := UnknownEnv([]string{"COLLECTOR_IMAGES_INTERVAL=1h"}); len(got) != 0 {
		t.Errorf("UnknownEnv = %v, want none", got)
	}
}

// The token and the token file are one option: the higher layer wins, both
// in one layer is an error, and an ignored lower one is reported.
func TestTokenAndTokenFile(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	emptyFile := filepath.Join(dir, "empty")
	if err := os.WriteFile(emptyFile, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configWithToken := filepath.Join(dir, "token.yaml")
	if err := os.WriteFile(configWithToken, []byte("api:\n  token: config-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configWithBoth := filepath.Join(dir, "both.yaml")
	if err := os.WriteFile(configWithBoth, []byte("api:\n  token: config-secret\n  token_file: "+tokenFile+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		args       []string
		env        map[string]string
		wantToken  string
		wantSource string
		wantWarn   string
		wantErr    string
	}{
		{name: "file from env over token in config file",
			args: []string{"--config.file", configWithToken}, env: map[string]string{"HCLOUD_API_TOKEN_FILE": tokenFile},
			wantToken: "file-secret", wantSource: "HCLOUD_API_TOKEN_FILE (" + tokenFile + ")",
			wantWarn: "api.token in " + configWithToken + " is ignored"},
		{name: "token flag over file from env",
			args: []string{"--hcloud.api-token=flag-secret"}, env: map[string]string{"HCLOUD_API_TOKEN_FILE": "/nonexistent"},
			wantToken: "flag-secret", wantSource: "--hcloud.api-token", wantWarn: "HCLOUD_API_TOKEN_FILE (/nonexistent) is ignored"},
		{name: "file flag", args: []string{"--hcloud.api-token-file", tokenFile},
			wantToken: "file-secret", wantSource: "--hcloud.api-token-file (" + tokenFile + ")"},
		{name: "token from config file", args: []string{"--config.file", configWithToken},
			wantToken: "config-secret", wantSource: "api.token in " + configWithToken},
		{name: "both in env", env: map[string]string{"HCLOUD_API_TOKEN": "t", "HCLOUD_API_TOKEN_FILE": tokenFile},
			wantErr: "both HCLOUD_API_TOKEN and HCLOUD_API_TOKEN_FILE are set"},
		{name: "both in config file", args: []string{"--config.file", configWithBoth},
			wantErr: "both api.token in " + configWithBoth + " and api.token_file in " + configWithBoth},
		{name: "missing file", env: map[string]string{"HCLOUD_API_TOKEN_FILE": filepath.Join(dir, "nope")},
			wantErr: "reading the token file"},
		{name: "empty file", env: map[string]string{"HCLOUD_API_TOKEN_FILE": emptyFile},
			wantErr: "token file " + emptyFile + " is empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			opts, err := Parse(tt.args, env(tt.env), io.Discard)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				if strings.Contains(err.Error(), "secret") {
					t.Errorf("error contains the token: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if opts.HetznerAPIToken != tt.wantToken || opts.TokenSource != tt.wantSource {
				t.Errorf("token %q from %q, want %q from %q", opts.HetznerAPIToken, opts.TokenSource, tt.wantToken, tt.wantSource)
			}
			warnings := strings.Join(opts.Warnings, "\n")
			if tt.wantWarn != "" && !strings.Contains(warnings, tt.wantWarn) {
				t.Errorf("warnings %q, want containing %q", warnings, tt.wantWarn)
			}
			if strings.Contains(warnings, "secret") {
				t.Errorf("warnings contain the token: %q", warnings)
			}
		})
	}
}
