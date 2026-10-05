// Package certificate exposes metrics for Hetzner Cloud TLS certificates.
package certificate

import (
	"context"
	"strconv"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/hcmetrics"
)

var descs = hcmetrics.NewDescSet("certificate", "certificate")

var (
	notValidAfterDesc = descs.New("not_valid_after_timestamp_seconds",
		"Unix timestamp when the certificate expires", "certificate_id", "certificate_type")
	statusDesc = descs.New("status",
		"Status of a managed certificate's issuance or renewal, always 1", "certificate_id", "process", "status")
	createdDesc = descs.New("created_timestamp_seconds", "Unix timestamp when the certificate was created")
)

// New returns the certificates source: expiry and status of uploaded and
// managed certificates, as used by Load Balancers.
func New(client *hcloud.Client) *hcmetrics.ListSource[*hcloud.Certificate] {
	return hcmetrics.NewListSource("certificates", descs,
		func(ctx context.Context) ([]*hcloud.Certificate, error) { return client.Certificate.All(ctx) },
		collect)
}

func collect(ch chan<- prometheus.Metric, cert *hcloud.Certificate) {
	id := strconv.FormatInt(cert.ID, 10)

	if ts, ok := hcmetrics.Timestamp(cert.Created); ok {
		ch <- prometheus.MustNewConstMetric(createdDesc, prometheus.GaugeValue, ts, cert.Name)
	}

	// Managed certificates have no expiry until they are issued.
	if !cert.NotValidAfter.IsZero() {
		ch <- prometheus.MustNewConstMetric(notValidAfterDesc, prometheus.GaugeValue,
			float64(cert.NotValidAfter.Unix()), cert.Name, id, string(cert.Type))
	}

	// Only managed certificates have an issuance/renewal status. A failed
	// issuance has no expiry at all, so this is the only way to see it.
	if status := cert.Status; status != nil {
		if status.Issuance != "" {
			ch <- prometheus.MustNewConstMetric(statusDesc, prometheus.GaugeValue, 1,
				cert.Name, id, "issuance", string(status.Issuance))
		}
		if status.Renewal != "" {
			ch <- prometheus.MustNewConstMetric(statusDesc, prometheus.GaugeValue, 1,
				cert.Name, id, "renewal", string(status.Renewal))
		}
	}
}
