// Package app wires the Hetzner client, the poller, the metrics registry and
// the HTTP server together.
package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	log "github.com/sirupsen/logrus"

	"github.com/prometheus/exporter-toolkit/web"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/buildinfo"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/logging"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/options"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/poller"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/sources"
)

// HTTP server limits. Scrapes are small and served from memory, so short
// timeouts are enough.
const (
	shutdownTimeout   = 5 * time.Second
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 60 * time.Second
)

// Run starts polling and serves metrics until ctx is cancelled or the HTTP
// server fails.
func Run(ctx context.Context, opts *options.Options) error {
	// Bind first: if the port is taken, fail before using any API budget.
	listener, err := net.Listen("tcp", opts.ListenAddress)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", opts.ListenAddress, err)
	}
	return run(ctx, opts, listener)
}

// run serves on listener; extra client options are used by tests to point
// the client at a fake API.
func run(ctx context.Context, opts *options.Options, listener net.Listener, extra ...hcloud.ClientOption) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	apiStats := poller.NewAPIStats(2 * opts.Concurrency)
	endpoint, err := url.Parse(hcloud.Endpoint)
	if err != nil {
		return fmt.Errorf("parsing the Hetzner API endpoint %q: %w", hcloud.Endpoint, err)
	}
	apiStats.SetRateLimitHost(endpoint.Host) // Storage Boxes (api.hetzner.com) have their own budget
	client := hcloud.NewClient(append([]hcloud.ClientOption{
		hcloud.WithToken(opts.HetznerAPIToken),
		hcloud.WithApplication(buildinfo.Name, buildinfo.Version),
		hcloud.WithHTTPClient(apiStats.HTTPClient()),
		// Retry transient errors a little. hcloud-go also retries a 429, but
		// APIStats stops those retries before they are sent (no budget used);
		// rate limiting is left to the poller's backoff.
		hcloud.WithRetryOpts(hcloud.RetryOpts{MaxRetries: 2}),
	}, extra...)...)

	hcloudPoller := poller.New(sources.Sources(client, sources.Config{
		Concurrency:     opts.Concurrency,
		MetricsInterval: opts.MetricsInterval,
		Enabled:         opts.Collectors,
		Intervals:       opts.Intervals,
	}), apiStats, opts.PollInterval)

	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	buildinfo.Register(registry)
	hcloudPoller.Register(registry)

	pollerDone := make(chan struct{})
	go func() {
		defer close(pollerDone)
		hcloudPoller.Run(ctx)
	}()

	server := &http.Server{
		Handler:           NewRouter(opts.MetricsPath, registry, hcloudPoller.Ready),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		// Connection-level errors go through logrus: setup problems as
		// warnings, scanner noise at debug level.
		ErrorLog: logging.HTTPErrorLog(),
	}

	// exporter-toolkit adds TLS and basic auth from --web.config.file; without
	// a file it serves plain HTTP.
	webFlags := &web.FlagConfig{
		WebListenAddresses: &[]string{opts.ListenAddress},
		WebSystemdSocket:   new(bool),
		WebConfigFile:      &opts.WebConfigFile,
	}
	serveErr := make(chan error, 1)
	logStartup(opts, listener.Addr().String())
	go func() {
		serveErr <- web.Serve(listener, server, webFlags, logging.Slog())
	}()

	var runErr error
	select {
	case <-ctx.Done():
		log.Info("Shutting down server")
	case err := <-serveErr:
		runErr = fmt.Errorf("HTTP server failed: %w", err)
	}
	cancel() // stops the poller and in-flight API requests

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.WithError(err).Warn("Graceful shutdown timed out, closing connections")
		_ = server.Close()
	}
	<-pollerDone

	if runErr == nil {
		log.Info("Server shutdown complete")
	}
	return runErr
}

// logStartup logs the effective configuration in one line.
func logStartup(opts *options.Options, addr string) {
	enabled := 0
	for _, on := range opts.Collectors {
		if on {
			enabled++
		}
	}
	configFile, webConfig, metrics := opts.ConfigLoaded, opts.WebConfigFile, opts.MetricsInterval.String()
	if configFile == "" {
		configFile = "none"
	}
	if webConfig == "" {
		webConfig = "none (plain HTTP, no authentication)"
	}
	if opts.MetricsInterval == 0 {
		metrics = "off"
	}
	log.WithFields(log.Fields{
		"address":          addr,
		"metrics_path":     opts.MetricsPath,
		"config_file":      configFile,
		"token":            opts.TokenSource,
		"web_config":       webConfig,
		"poll_interval":    opts.PollInterval.String(),
		"metrics_interval": metrics,
		"collectors":       fmt.Sprintf("%d of %d", enabled, len(opts.Collectors)),
	}).Info("Starting Prometheus exporter")
}
