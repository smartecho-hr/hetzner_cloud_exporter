package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/buildinfo"
)

// Readiness reports whether the exporter has data, and if not, what is missing.
type Readiness func() (ready bool, missing []string)

// NewRouter returns the HTTP handler serving metrics, version and health endpoints.
// Limits for /metrics: more parallel scrapes get HTTP 503, a scrape taking
// longer than scrapeTimeout gets HTTP 503 too.
const (
	maxScrapesInFlight = 10
	scrapeTimeout      = 10 * time.Second
)

// metricsPath must be validated (see options), otherwise ServeMux panics.
func NewRouter(metricsPath string, registry *prometheus.Registry, ready Readiness) http.Handler {
	mux := http.NewServeMux()

	mux.Handle(metricsPath, promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		// A broken metric must not hide the others, e.g. the exporter health metrics.
		ErrorHandling:       promhttp.ContinueOnError,
		ErrorLog:            log.StandardLogger(),
		MaxRequestsInFlight: maxScrapesInFlight,
		Timeout:             scrapeTimeout,
		Registry:            registry, // promhttp_metric_handler_* metrics
	}))

	mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(buildinfo.Map())
	})

	// Liveness: the HTTP server runs.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Readiness: every source has fetched its data at least once.
	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		if ok, missing := ready(); !ok {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprintf(w, "not ready, waiting for the first successful poll of: %s\n", strings.Join(missing, ", "))
			return
		}
		_, _ = w.Write([]byte("ready"))
	})

	// Root fallback, unless metrics are served at the root
	if metricsPath != "/" {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprintf(w, "404 not found. Metrics are at %s, health at /healthz and /ready, build info at /version.\n", metricsPath)
		})
	}

	return mux
}
