package certificate

import (
	"context"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/smartecho-hr/hetzner_cloud_exporter/internal/hcloudtest"
)

func TestCollect(t *testing.T) {
	_, client := hcloudtest.NewServer(t, map[string]string{
		"/certificates": `{"certificates": [
			{"id": 1, "name": "uploaded", "type": "uploaded", "not_valid_after": "2027-01-01T00:00:00Z", "status": null},
			{"id": 2, "name": "pending", "type": "managed", "not_valid_after": null,
			 "status": {"issuance": "pending", "renewal": "unavailable"}},
			{"id": 3, "name": "failed", "type": "managed", "not_valid_after": null,
			 "status": {"issuance": "failed", "renewal": "unavailable", "error": {"code": "dns_zone_not_found", "message": "zone not found"}}},
			{"id": 4, "name": "renewing", "type": "managed", "not_valid_after": "2026-11-01T00:00:00Z",
			 "status": {"issuance": "completed", "renewal": "failed"}}
		], ` + hcloudtest.Pagination + `}`,
	})
	source := New(client)

	if err := source.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	expected := `
# HELP hetzner_cloud_certificate_not_valid_after_timestamp_seconds Unix timestamp when the certificate expires
# TYPE hetzner_cloud_certificate_not_valid_after_timestamp_seconds gauge
hetzner_cloud_certificate_not_valid_after_timestamp_seconds{certificate="renewing",certificate_id="4",certificate_type="managed"} 1.7934912e+09
hetzner_cloud_certificate_not_valid_after_timestamp_seconds{certificate="uploaded",certificate_id="1",certificate_type="uploaded"} 1.7987616e+09
# HELP hetzner_cloud_certificate_status Status of a managed certificate's issuance or renewal, always 1
# TYPE hetzner_cloud_certificate_status gauge
hetzner_cloud_certificate_status{certificate="failed",certificate_id="3",process="issuance",status="failed"} 1
hetzner_cloud_certificate_status{certificate="failed",certificate_id="3",process="renewal",status="unavailable"} 1
hetzner_cloud_certificate_status{certificate="pending",certificate_id="2",process="issuance",status="pending"} 1
hetzner_cloud_certificate_status{certificate="pending",certificate_id="2",process="renewal",status="unavailable"} 1
hetzner_cloud_certificate_status{certificate="renewing",certificate_id="4",process="issuance",status="completed"} 1
hetzner_cloud_certificate_status{certificate="renewing",certificate_id="4",process="renewal",status="failed"} 1
`
	if err := testutil.CollectAndCompare(source, strings.NewReader(expected)); err != nil {
		t.Error(err)
	}
	if problems, err := testutil.CollectAndLint(source); err != nil || len(problems) > 0 {
		t.Errorf("lint: err=%v problems=%v", err, problems)
	}
}
