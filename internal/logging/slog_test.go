package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
)

func TestSlogAdapter(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.SetFormatter(&lineFormatter{})
	log.SetLevel(log.InfoLevel)
	t.Cleanup(func() { log.SetOutput(nil) })

	l := Slog().With("before", 1).WithGroup("g").With("inner", 2)
	l.Info("hello", "k", "v", slog.Group("req", "method", "GET"), "", "dropped")
	l.Debug("hidden")

	out := buf.String()
	for _, want := range []string{
		`info: hello`, `before="1"`, `g.inner="2"`, `g.k="v"`, `g.req.method="GET"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
	for _, unwanted := range []string{`g.before`, `="dropped"`, `hidden`} {
		if strings.Contains(out, unwanted) {
			t.Errorf("output %q must not contain %q", out, unwanted)
		}
	}
}
