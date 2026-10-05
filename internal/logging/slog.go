package logging

import (
	"context"
	"log/slog"

	log "github.com/sirupsen/logrus"
)

// Slog returns a slog.Logger that writes through logrus, for libraries
// that log with slog (exporter-toolkit), so all output has one format.
func Slog() *slog.Logger {
	return slog.New(&logrusHandler{})
}

// logrusHandler implements slog.Handler. Attributes are stored with their
// full key (groups as "group.key") when they are added, so a group only
// applies to attributes added after it.
type logrusHandler struct {
	fields log.Fields
	group  string // prefix for attributes added from now on
}

func (h *logrusHandler) Enabled(_ context.Context, level slog.Level) bool {
	return log.IsLevelEnabled(toLogrus(level))
}

func (h *logrusHandler) Handle(_ context.Context, r slog.Record) error {
	fields := make(log.Fields, len(h.fields)+r.NumAttrs())
	for k, v := range h.fields {
		fields[k] = v
	}
	r.Attrs(func(a slog.Attr) bool {
		addAttr(fields, h.group, a)
		return true
	})
	log.WithFields(fields).Log(toLogrus(r.Level), r.Message)
	return nil
}

func (h *logrusHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	fields := make(log.Fields, len(h.fields)+len(attrs))
	for k, v := range h.fields {
		fields[k] = v
	}
	for _, a := range attrs {
		addAttr(fields, h.group, a)
	}
	return &logrusHandler{fields: fields, group: h.group}
}

func (h *logrusHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &logrusHandler{fields: h.fields, group: h.group + name + "."}
}

// addAttr adds an attribute following slog's rules: values are resolved,
// groups are flattened into "group.key", empty keys are dropped (an empty
// group key inlines its attributes).
func addAttr(fields log.Fields, prefix string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Value.Kind() == slog.KindGroup {
		groupPrefix := prefix
		if a.Key != "" {
			groupPrefix += a.Key + "."
		}
		for _, ga := range a.Value.Group() {
			addAttr(fields, groupPrefix, ga)
		}
		return
	}
	if a.Key == "" {
		return
	}
	fields[prefix+a.Key] = a.Value.Any()
}

func toLogrus(level slog.Level) log.Level {
	switch {
	case level >= slog.LevelError:
		return log.ErrorLevel
	case level >= slog.LevelWarn:
		return log.WarnLevel
	case level >= slog.LevelInfo:
		return log.InfoLevel
	default:
		return log.DebugLevel
	}
}
