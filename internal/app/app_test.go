package app

import (
	"context"
	"time"

	"encoding/json"
	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/hcloudtest"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/buildinfo"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/options"
)

func get(t *testing.T, handler http.Handler, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	body, _ := io.ReadAll(rec.Body)
	return rec.Code, string(body)
}

func TestRouter(t *testing.T) {
	registry := prometheus.NewRegistry()
	buildinfo.Register(registry)
	router := NewRouter("/metrics", registry, func() (bool, []string) { return true, nil })

	if code, body := get(t, router, "/metrics"); code != 200 || !strings.Contains(body, "hetzner_cloud_exporter_build_info") {
		t.Errorf("/metrics: %d, missing build info:\n%s", code, body)
	}
	if code, body := get(t, router, "/healthz"); code != 200 || body != "ok" {
		t.Errorf("/healthz: %d %q", code, body)
	}
	if code, body := get(t, router, "/nope"); code != 404 || !strings.Contains(body, "/metrics") {
		t.Errorf("/nope: %d %q", code, body)
	}

	code, body := get(t, router, "/version")
	var info map[string]string
	if err := json.Unmarshal([]byte(body), &info); code != 200 || err != nil || info["name"] != buildinfo.Name {
		t.Errorf("/version: %d %q (%v)", code, body, err)
	}
}

func TestRouterMetricsAtRoot(t *testing.T) {
	router := NewRouter("/", prometheus.NewRegistry(), func() (bool, []string) { return true, nil })
	if code, _ := get(t, router, "/"); code != 200 {
		t.Errorf("metrics at /: status %d", code)
	}
}

func TestRunFailsWhenPortIsTaken(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()

	opts := &options.Options{ListenAddress: taken.Addr().String(), MetricsPath: "/metrics", HetznerAPIToken: "x"}
	err = Run(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "listening on") {
		t.Errorf("err = %v, want listen error", err)
	}
}

func TestReady(t *testing.T) {
	ready, missing := false, []string{"servers", "certificates"}
	router := NewRouter("/metrics", prometheus.NewRegistry(), func() (bool, []string) { return ready, missing })

	if code, body := get(t, router, "/ready"); code != 503 || !strings.Contains(body, "servers, certificates") {
		t.Errorf("not ready: %d %q", code, body)
	}
	ready, missing = true, nil
	if code, body := get(t, router, "/ready"); code != 200 || body != "ready" {
		t.Errorf("ready: %d %q", code, body)
	}
}

func TestHealthCheck(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	env := func(key string) (string, bool) {
		if key == "LISTEN_ADDRESS" {
			return addr, true
		}
		return "", false
	}

	noEnv := func(string) (string, bool) { return "", false }
	_, port, _ := net.SplitHostPort(addr)
	configFile := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configFile, []byte("server:\n  host: 127.0.0.1\n  port: "+port+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The address comes from the environment, a flag or the config file, as
	// for the exporter itself; the token is not needed.
	cases := []struct {
		name string
		args []string
		env  func(string) (string, bool)
	}{
		{"env", nil, env},
		{"flag", []string{"--web.listen-address=" + addr}, noEnv},
		{"config file", []string{"--config.file=" + configFile}, noEnv},
	}
	for _, c := range cases {
		if code := HealthCheck(c.args, c.env); code != 0 {
			t.Errorf("%s, listening: exit code %d, want 0", c.name, code)
		}
	}
	l.Close()
	for _, c := range cases {
		if code := HealthCheck(c.args, c.env); code != 1 {
			t.Errorf("%s, not listening: exit code %d, want 1", c.name, code)
		}
	}
}

// The healthcheck finds the exporter's flags also when PID 1 is an init
// process, and skips itself and other healthchecks.
func TestExporterArgs(t *testing.T) {
	proc := t.TempDir()
	write := func(pid int, argv ...string) {
		dir := filepath.Join(proc, strconv.Itoa(pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(strings.Join(argv, "\x00")+"\x00"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(1, "/sbin/docker-init", "--", "/usr/local/bin/hetzner_cloud_exporter", "--web.listen-address=:9300")
	write(7, "/usr/local/bin/hetzner_cloud_exporter", "--web.listen-address=:9300")
	write(12, "/usr/local/bin/hetzner_cloud_exporter", "healthcheck")
	write(13, "/usr/local/bin/hetzner_cloud_exporter")
	if err := os.MkdirAll(filepath.Join(proc, "self"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := exporterArgs(proc, 13, "hetzner_cloud_exporter")
	if strings.Join(got, " ") != "--web.listen-address=:9300" {
		t.Errorf("args = %q, want the flags of PID 7", got)
	}
	if got := exporterArgs(proc, 13, "other"); got != nil {
		t.Errorf("no matching process: args = %q, want nil", got)
	}
	if got := exporterArgs(filepath.Join(proc, "missing"), 1, "x"); got != nil {
		t.Errorf("no /proc: args = %q, want nil", got)
	}
}

// TestRunEndToEnd runs the whole exporter against a fake Hetzner API:
// polling, /ready, /metrics and a clean shutdown.
func TestRunEndToEnd(t *testing.T) {
	fake, _ := hcloudtest.NewServer(t, map[string]string{
		"/servers": `{"servers": [{"id": 10, "name": "web-1", "status": "running", "server_type": {"name": "cx22", "cores": 2},
			"location": {"name": "fsn1"}}], ` + hcloudtest.Pagination + `}`,
		"/servers/10/metrics": `{"metrics": {"start": "2026-01-01T00:00:00Z", "end": "2026-01-01T00:01:00Z", "step": 60,
			"time_series": {"cpu": {"values": [[1767225660, "50"]]}}}}`,
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + listener.Addr().String()

	opts := &options.Options{
		ListenAddress: listener.Addr().String(), MetricsPath: "/metrics", HetznerAPIToken: "test",
		PollInterval: time.Minute, MetricsInterval: time.Minute, Concurrency: 2,
		Collectors: map[string]bool{"servers": true}, // only the source the fake API serves
	}
	for _, c := range []string{"pricing", "load_balancers", "certificates", "volumes", "floating_ips", "primary_ips", "images", "storage_boxes", "ssh_keys"} {
		opts.Collectors[c] = false
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, opts, listener, hcloud.WithEndpoint(fake.URL), hcloud.WithRetryOpts(hcloud.RetryOpts{MaxRetries: 0}))
	}()

	httpGet := func(path string) (int, string) {
		resp, err := http.Get(base + path)
		if err != nil {
			return 0, err.Error()
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if code, _ := httpGet("/ready"); code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			_, body := httpGet("/ready")
			t.Fatalf("not ready after 5s: %s", body)
		}
		time.Sleep(20 * time.Millisecond)
	}

	code, body := httpGet("/metrics")
	for _, want := range []string{`hetzner_cloud_server_info{location=`, `server_id="10"`, `hetzner_cloud_server_cpu_usage_ratio{server="web-1"} 0.25`,
		`hetzner_cloud_exporter_last_poll_success{source="servers"} 1`, "go_goroutines ", "process_start_time_seconds "} {
		if code != http.StatusOK || !strings.Contains(body, want) {
			t.Errorf("/metrics (%d) missing %q", code, want)
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not stop after cancel")
	}
}
