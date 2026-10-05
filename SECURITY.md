# Security policy

## Reporting a vulnerability

Please report vulnerabilities privately, not in a public issue: use GitHub's
*Report a vulnerability* button on the repository's *Security* tab. Only the maintainers see the
report until a fix is released.

Include the version (`hetzner_cloud_exporter --version`), how to reproduce the problem and
what an attacker could do with it. You get an answer within a week.

Only the latest release gets security fixes.

## Running the exporter safely

- Use a **read-only** Hetzner API token; the exporter never writes.
- Pass the token through `HCLOUD_API_TOKEN`, a token file (`HCLOUD_API_TOKEN_FILE`, e.g. a
  Docker/Kubernetes secret) or a config file only readable by the exporter's user, not on the
  command line (visible in the process list).
- Without `--web.config.file`, `/metrics` has no authentication. Bind to `127.0.0.1` or a
  private network, or enable TLS together with basic auth.
- The exporter never logs or exposes the token, and only sends it to Hetzner's API hosts.
