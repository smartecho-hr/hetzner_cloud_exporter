package logging

import (
	"errors"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
)

func TestLineFormatter(t *testing.T) {
	entry := log.WithFields(log.Fields{
		"source": "servers",
		"error":  errors.New(`listing "servers": boom`),
	})
	entry.Time = time.Date(2026, 10, 1, 18, 11, 15, 0, time.UTC)
	entry.Level = log.ErrorLevel
	entry.Message = "Failed to poll source"

	out, err := (&lineFormatter{}).Format(entry)
	if err != nil {
		t.Fatal(err)
	}

	// Fields must be included (sorted, quoted); they were dropped before.
	want := `[2026/10/01 18:11:15] error: Failed to poll source error="listing \"servers\": boom" source="servers"` + "\n"
	if string(out) != want {
		t.Errorf("Format =\n  %q\nwant\n  %q", out, want)
	}
}
