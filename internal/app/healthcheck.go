package app

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/buildinfo"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/options"
)

// HealthCheck implements `hetzner_cloud_exporter healthcheck [flags]` for
// container health checks: it connects to the listen address and returns the
// exit code. A TCP connect works with TLS and basic auth enabled, unlike an
// HTTP request to /healthz.
//
// The address is resolved like the exporter does it: flags, environment
// (LISTEN_ADDRESS) and config file. Without flags, the flags of the running
// exporter are used (on Linux, from /proc), so
// `docker run image --web.listen-address=:9300` works with the image's
// HEALTHCHECK, also behind an init process (docker run --init, tini).
func HealthCheck(args []string, lookupEnv func(string) (string, bool)) int {
	if len(args) == 0 {
		args = exporterArgs("/proc", os.Getpid(), filepath.Base(os.Args[0]))
	}
	address, err := options.ListenAddress(args, lookupEnv)
	switch {
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprintln(os.Stdout, "Usage: hetzner_cloud_exporter healthcheck [flags]\n"+
			"Exits 0 if the exporter accepts TCP connections on its listen address. Takes the exporter's\n"+
			"flags, environment and config file (see hetzner_cloud_exporter --help); without flags it uses\n"+
			"those of the running exporter.")
		return 0
	case errors.Is(err, options.ErrVersion):
		buildinfo.Print(os.Stdout)
		return 0
	case err != nil:
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}

	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 3*time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	_ = conn.Close()
	return 0
}

// exporterArgs returns the command line flags of the running exporter: the
// process with the lowest PID whose program is called name, other than self
// and other healthchecks. In a container that is PID 1, or the child of an
// init process. nil if there is none or procDir doesn't exist (not Linux).
func exporterArgs(procDir string, self int, name string) []string {
	entries, err := os.ReadDir(procDir)
	if err != nil {
		return nil
	}
	var pids []int
	for _, e := range entries {
		if pid, err := strconv.Atoi(e.Name()); err == nil && pid != self {
			pids = append(pids, pid)
		}
	}
	slices.Sort(pids)
	for _, pid := range pids {
		data, err := os.ReadFile(filepath.Join(procDir, strconv.Itoa(pid), "cmdline"))
		if err != nil || len(data) == 0 {
			continue
		}
		argv := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
		if filepath.Base(argv[0]) != name || (len(argv) > 1 && argv[1] == "healthcheck") {
			continue
		}
		return argv[1:]
	}
	return nil
}
