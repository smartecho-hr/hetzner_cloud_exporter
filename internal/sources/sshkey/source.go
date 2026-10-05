// Package sshkey exposes metrics for Hetzner Cloud SSH keys.
package sshkey

import (
	"context"
	"strconv"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/hcmetrics"
)

var descs = hcmetrics.NewDescSet("ssh_key", "ssh_key")

var (
	infoDesc    = descs.New("info", "SSH key information, always 1", "ssh_key_id", "fingerprint")
	createdDesc = descs.New("created_timestamp_seconds", "Unix timestamp when the SSH key was added")
)

// New returns the SSH keys source.
func New(client *hcloud.Client) *hcmetrics.ListSource[*hcloud.SSHKey] {
	return hcmetrics.NewListSource("ssh_keys", descs,
		func(ctx context.Context) ([]*hcloud.SSHKey, error) { return client.SSHKey.All(ctx) },
		collect)
}

func collect(ch chan<- prometheus.Metric, key *hcloud.SSHKey) {
	ch <- prometheus.MustNewConstMetric(infoDesc, prometheus.GaugeValue, 1,
		key.Name, strconv.FormatInt(key.ID, 10), key.Fingerprint)
	if ts, ok := hcmetrics.Timestamp(key.Created); ok {
		ch <- prometheus.MustNewConstMetric(createdDesc, prometheus.GaugeValue, ts, key.Name)
	}
}
