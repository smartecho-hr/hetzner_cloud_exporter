package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFile(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadServerAndToken(t *testing.T) {
	path := writeFile(t, `
server:
  host: "0.0.0.0"
  port: 9210
api:
  token: "file-token"
`, 0o600)
	cfg, err := Load(path, false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := map[string]string{
		"web.listen-address": "0.0.0.0:9210",
		"hcloud.api-token":   "file-token",
	}
	if got := cfg.FlagValues(); !reflect.DeepEqual(got, want) {
		t.Errorf("FlagValues = %v, want %v", got, want)
	}
	if w := cfg.Warnings(); len(w) != 0 {
		t.Errorf("Warnings = %v, want none", w)
	}
}

// Unknown blocks get one warning each, without their values (e.g. a password).
func TestUnknownBlocksDontLeakValues(t *testing.T) {
	cfg, err := Load(writeFile(t, "proxy:\n  username: admin\n  password: secret123\nextra:\n  ttl: 60\n", 0o600), false)
	if err != nil {
		t.Fatal(err)
	}
	w := strings.Join(cfg.Warnings(), "\n")
	for _, want := range []string{`unknown key "proxy"`, `unknown key "extra"`} {
		if !strings.Contains(w, want) {
			t.Errorf("warnings %q missing %q", w, want)
		}
	}
	if strings.Contains(w, "secret123") {
		t.Errorf("warnings contain the password: %q", w)
	}
}

func TestLoadNewOptions(t *testing.T) {
	path := writeFile(t, `
server:
  port: 9300
  metrics_path: /m
api:
  token: t
  poll_interval: 90
  concurrency: 3
log:
  level: debug
`, 0o600)
	cfg, err := Load(path, false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := map[string]string{
		"web.listen-address":   ":9300",
		"web.metrics-path":     "/m",
		"hcloud.api-token":     "t",
		"hcloud.poll-interval": "90s", // bare number means seconds
		"hcloud.concurrency":   "3",
		"log.level":            "debug",
	}
	if got := cfg.FlagValues(); !reflect.DeepEqual(got, want) {
		t.Errorf("FlagValues = %v, want %v", got, want)
	}
	if len(cfg.Warnings()) != 0 {
		t.Errorf("Warnings = %v, want none", cfg.Warnings())
	}
}

func TestListenAddress(t *testing.T) {
	tests := []struct {
		yaml string
		want string
	}{
		{"server:\n  host: 127.0.0.1\n", "127.0.0.1:9210"},
		{"server:\n  port: 9300\n", ":9300"},
		{"server:\n  host: \"::1\"\n", "[::1]:9210"},
		{"server:\n  host: \"[::1]\"\n  port: 9300\n", "[::1]:9300"},
	}
	for _, tt := range tests {
		cfg, err := Load(writeFile(t, tt.yaml, 0o600), false)
		if err != nil {
			t.Fatalf("Load(%q): %v", tt.yaml, err)
		}
		if got := cfg.FlagValues()["web.listen-address"]; got != tt.want {
			t.Errorf("%q: listen address = %q, want %q", tt.yaml, got, tt.want)
		}
	}
}

func TestLoadWarnsAboutUnknownKeys(t *testing.T) {
	path := writeFile(t, "api:\n  token: t\n  poll_intervall: 2m\n", 0o600)
	cfg, err := Load(path, false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w := cfg.Warnings(); len(w) != 1 || !strings.Contains(w[0], "poll_intervall") {
		t.Errorf("Warnings = %v, want one about poll_intervall", w)
	}
}

func TestLoadWarnsAboutReadableToken(t *testing.T) {
	path := writeFile(t, "api:\n  token: t\n", 0o644)
	cfg, err := Load(path, false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w := cfg.Warnings(); len(w) != 1 || !strings.Contains(w[0], "chmod 600") {
		t.Errorf("Warnings = %v, want one about permissions", w)
	}
}

func TestLoadMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.yaml")

	cfg, err := Load(missing, false)
	if err != nil || cfg != nil {
		t.Errorf("optional missing file: cfg=%v err=%v, want nil, nil", cfg, err)
	}
	if _, err := Load(missing, true); err == nil {
		t.Error("required missing file: want error")
	}
}

func TestLoadEmptyFile(t *testing.T) {
	cfg, err := Load(writeFile(t, "", 0o600), false)
	if err != nil || len(cfg.Warnings()) != 0 || len(cfg.FlagValues()) != 0 {
		t.Errorf("empty file: err=%v warnings=%v values=%v", err, cfg.Warnings(), cfg.FlagValues())
	}
}

func TestLoadInvalidYAML(t *testing.T) {
	if _, err := Load(writeFile(t, "server: [\n", 0o600), false); err == nil {
		t.Error("want parse error")
	}
}

func TestDuplicateCollectorKeys(t *testing.T) {
	path := writeFile(t, "collectors:\n  load_balancers: true\n  load-balancers: false\n", 0o600)
	if _, err := Load(path, false); err == nil || !strings.Contains(err.Error(), "same collector") {
		t.Errorf("err = %v, want duplicate collector error", err)
	}
}

func TestYAMLErrorsAreReadable(t *testing.T) {
	// Unknown keys: one readable warning each, no Go types.
	cfg, err := Load(writeFile(t, "api:\n  token: t\n  tokn: x\nlogs:\n  level: info\n", 0o600), false)
	if err != nil {
		t.Fatal(err)
	}
	w := strings.Join(cfg.Warnings(), "\n")
	for _, want := range []string{`unknown key "tokn"`, `unknown key "logs"`} {
		if !strings.Contains(w, want) {
			t.Errorf("warnings %q missing %q", w, want)
		}
	}
	if strings.Contains(w, "struct") {
		t.Errorf("warnings mention Go types: %q", w)
	}

	// Type errors don't show Go types either.
	_, err = Load(writeFile(t, "api: just-a-string\n", 0o600), false)
	if err == nil || strings.Contains(err.Error(), "struct") || !strings.Contains(err.Error(), "wrong type") {
		t.Errorf("err = %v, want a readable type error", err)
	}

	// A token in a wrong place must not be echoed.
	_, err = Load(writeFile(t, "api:\n  concurrency: secret-token-value\n", 0o600), false)
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Errorf("err = %v, want an error without the value", err)
	}
}

func TestCollectorIntervals(t *testing.T) {
	cfg, err := Load(writeFile(t, "collector_intervals:\n  images: 30m\n  load_balancers: 90\n", 0o600), false)
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.FlagValues()
	if got["collector.images.interval"] != "30m" || got["collector.load-balancers.interval"] != "90s" {
		t.Errorf("FlagValues = %v", got)
	}

	path := writeFile(t, "collector_intervals:\n  load_balancers: 1m\n  load-balancers: 2m\n", 0o600)
	if _, err := Load(path, false); err == nil || !strings.Contains(err.Error(), "collector_intervals") {
		t.Errorf("err = %v, want duplicate collector error", err)
	}
}
