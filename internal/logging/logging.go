// Package logging configures the global logrus logger.
package logging

import (
	"fmt"
	stdlog "log"
	"sort"
	"strings"

	log "github.com/sirupsen/logrus"
)

// lineFormatter writes one line per entry:
//
//	[2006/01/02 15:04:05] level: message key="value" ...
type lineFormatter struct{}

func (f *lineFormatter) Format(entry *log.Entry) ([]byte, error) {
	timestamp := entry.Time.Format("2006/01/02 15:04:05")
	level := entry.Level.String()

	var b strings.Builder
	fmt.Fprintf(&b, "[%s] %s: %s", timestamp, level, entry.Message)

	// Append structured fields (e.g. WithError, WithField) in a stable order
	keys := make([]string, 0, len(entry.Data))
	for key := range entry.Data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(&b, " %s=%q", key, fmt.Sprint(entry.Data[key]))
	}

	b.WriteByte('\n')
	return []byte(b.String()), nil
}

// InitLogger configures the global logger. An invalid level falls back to info.
func InitLogger(level string) {
	log.SetFormatter(&lineFormatter{})

	parsed, err := log.ParseLevel(level)
	if err != nil {
		log.SetLevel(log.InfoLevel)
		log.WithField("level", level).Warn("Invalid log level, using info")
		return
	}
	log.SetLevel(parsed)
}

// HTTPErrorLog returns the ErrorLog for an http.Server: real problems
// (handler panics, accept errors, a broken TLS certificate or key) are logged
// as warnings, connection noise from clients and scanners (EOF, plain HTTP to
// the HTTPS port, unsupported TLS versions) only at debug level.
func HTTPErrorLog() *stdlog.Logger {
	return stdlog.New(httpErrorWriter{}, "", 0)
}

type httpErrorWriter struct{}

// serverSide marks errors caused by the exporter's own setup, not the client.
var serverSide = []string{"panic", "Accept error", "failed to load", "X509KeyPair", "no such file", "permission denied", "private key"}

func (httpErrorWriter) Write(p []byte) (int, error) {
	line := strings.TrimSpace(string(p))
	for _, marker := range serverSide {
		if strings.Contains(line, marker) {
			log.Warn("HTTP server: " + line)
			return len(p), nil
		}
	}
	log.Debug("HTTP server: " + line)
	return len(p), nil
}
