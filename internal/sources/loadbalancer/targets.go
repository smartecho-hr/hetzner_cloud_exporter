package loadbalancer

import (
	"slices"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	log "github.com/sirupsen/logrus"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/hcmetrics"
)

// Labels identifying a target: target_type is "server" or "ip". For server
// targets, server/server_id identify the server and ip is set when the
// target uses a specific IP (e.g. IPv6). For IP targets, ip is the address.
var targetLabels = []string{"target_type", "server", "server_id", "ip"}

var (
	targetHealthyDesc = descs.New("target_healthy",
		"Health of a target for one Load Balancer service (1=healthy, 0=unhealthy or unknown)",
		slices.Concat(targetLabels, []string{"listen_port"})...)
	targetHealthStatusDesc = descs.New("target_health_status",
		"Health check result of a target for one service, always 1. detail and http_status_code explain why a target is unhealthy",
		slices.Concat(targetLabels, []string{"listen_port", "status", "detail", "http_status_code"})...)
	targetUnhealthyServicesDesc = descs.New("target_unhealthy_services",
		"Number of Load Balancer services for which the target is unhealthy (unknown is not counted)",
		targetLabels...)
)

const (
	targetTypeServer = "server"
	targetTypeIP     = "ip"
)

type target struct {
	targetType string
	serverID   int64
	ip         string
	health     []rawHealthStatus
}

// resolveTargets expands label selector targets into the servers they match.
// A server that is both a direct target and matched by a label selector is
// returned once; the same server with different IPs stays separate.
func resolveTargets(lbName string, raw []rawTarget) []target {
	type key struct {
		targetType string
		serverID   int64
		ip         string
	}
	seen := make(map[key]bool)
	var resolved []target

	var visit func([]rawTarget)
	visit = func(targets []rawTarget) {
		for _, rt := range targets {
			var t target
			switch {
			case rt.Type == "label_selector":
				visit(rt.Targets)
				continue
			case rt.Type == targetTypeServer && rt.Server != nil:
				t = target{targetType: targetTypeServer, serverID: rt.Server.ID, ip: rt.Server.IP}
			case rt.Type == targetTypeIP && rt.IP != nil && rt.IP.IP != "":
				t = target{targetType: targetTypeIP, ip: rt.IP.IP}
			default:
				log.WithFields(log.Fields{"load_balancer": lbName, "target_type": rt.Type}).Warn("Unable to resolve target")
				continue
			}

			k := key{t.targetType, t.serverID, t.ip}
			if seen[k] {
				continue
			}
			seen[k] = true
			t.health = rt.HealthStatus
			resolved = append(resolved, t)
		}
	}
	visit(raw)
	return resolved
}

// labelValues returns the values for targetLabels. serverName resolves a
// server ID to its name and may be nil.
func (t target) labelValues(serverName func(int64) string) []string {
	if t.targetType == targetTypeIP {
		return []string{targetTypeIP, "", "", t.ip}
	}
	name := ""
	if serverName != nil {
		name = serverName(t.serverID)
	}
	return []string{targetTypeServer, name, strconv.FormatInt(t.serverID, 10), t.ip}
}

func collectTargets(ch chan<- prometheus.Metric, lbName string, targets []target, serverName func(int64) string) {
	for _, t := range targets {
		labels := slices.Concat([]string{lbName}, t.labelValues(serverName))

		unhealthy := 0
		seenPorts := make(map[int]bool)
		for _, h := range t.health {
			if seenPorts[h.ListenPort] {
				continue // the API should report each service once; avoid duplicate series
			}
			seenPorts[h.ListenPort] = true
			if h.Status == "unhealthy" {
				unhealthy++
			}
			port := strconv.Itoa(h.ListenPort)
			httpStatus := ""
			if h.HTTPStatusCode != 0 {
				httpStatus = strconv.Itoa(h.HTTPStatusCode)
			}

			ch <- prometheus.MustNewConstMetric(targetHealthyDesc, prometheus.GaugeValue,
				hcmetrics.BoolToFloat(h.Status == "healthy"), slices.Concat(labels, []string{port})...)
			ch <- prometheus.MustNewConstMetric(targetHealthStatusDesc, prometheus.GaugeValue, 1,
				slices.Concat(labels, []string{port, h.Status, h.Detail, httpStatus})...)
		}
		ch <- prometheus.MustNewConstMetric(targetUnhealthyServicesDesc, prometheus.GaugeValue,
			float64(unhealthy), labels...)
	}
}
