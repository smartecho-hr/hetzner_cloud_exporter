package sshkey

import (
	"context"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/hcloudtest"
)

func TestCollect(t *testing.T) {
	_, client := hcloudtest.NewServer(t, map[string]string{
		"/ssh_keys": `{"ssh_keys": [{"id": 1, "name": "alan", "fingerprint": "b7:2f:30:a0:2f:6c:58:6c:21:04:58:61:ba:06:3b:2f",
			"public_key": "ssh-ed25519 AAAA", "created": "2026-01-01T00:00:00Z"}], ` + hcloudtest.Pagination + `}`,
	})
	source := New(client)
	if err := source.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	expected := `
# HELP hetzner_cloud_ssh_key_created_timestamp_seconds Unix timestamp when the SSH key was added
# TYPE hetzner_cloud_ssh_key_created_timestamp_seconds gauge
hetzner_cloud_ssh_key_created_timestamp_seconds{ssh_key="alan"} 1.7672256e+09
# HELP hetzner_cloud_ssh_key_info SSH key information, always 1
# TYPE hetzner_cloud_ssh_key_info gauge
hetzner_cloud_ssh_key_info{fingerprint="b7:2f:30:a0:2f:6c:58:6c:21:04:58:61:ba:06:3b:2f",ssh_key="alan",ssh_key_id="1"} 1
`
	if err := testutil.CollectAndCompare(source, strings.NewReader(expected)); err != nil {
		t.Error(err)
	}
}
