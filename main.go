// Command hetzner_cloud_exporter is a Prometheus exporter for Hetzner Cloud.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	log "github.com/sirupsen/logrus"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/app"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/buildinfo"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/logging"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/options"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(app.HealthCheck(os.Args[2:], os.LookupEnv))
	}

	opts, err := options.Parse(os.Args[1:], os.LookupEnv, os.Stderr)
	switch {
	case errors.Is(err, options.ErrVersion):
		buildinfo.Print(os.Stdout)
		return
	case errors.Is(err, flag.ErrHelp):
		return
	case err != nil:
		fmt.Fprintln(os.Stderr, "Error:", err)
		fmt.Fprintln(os.Stderr, "Run with --help to see all options.")
		os.Exit(1)
	}

	logging.InitLogger(opts.LogLevel)
	for _, warning := range append(opts.Warnings, options.UnknownEnv(os.Environ())...) {
		log.Warn(warning)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop() // a second Ctrl-C now terminates immediately
	}()
	err = app.Run(ctx, opts)
	stop()
	if err != nil {
		log.WithError(err).Error("Exporter stopped")
		os.Exit(1)
	}
}
