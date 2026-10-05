# Keep the Go version in sync with the go line in go.mod. Base images are
# pinned by digest (tag kept for readability); update both together.
#
# Build from the repository root:
#   docker build -f docker/Dockerfile -t hetzner-cloud-exporter \
#     --build-arg VERSION=$(git describe --tags --always) \
#     --build-arg COMMIT=$(git rev-parse --short HEAD) \
#     --build-arg BRANCH=$(git rev-parse --abbrev-ref HEAD) \
#     --build-arg SOURCE_DATE_EPOCH=$(git log -1 --format=%ct) .
# The build context only contains the Go sources (see .dockerignore).
# The build date is the commit time (SOURCE_DATE_EPOCH), so builds of the same
# commit are reproducible; without it, the date is "unknown".
#
# Multi-arch (docker buildx build --platform ...): the build stage always runs on the build
# machine's platform and Go cross-compiles for the target, so arm64 images are
# built without emulation. The final stage only copies the binary.

FROM --platform=$BUILDPLATFORM golang:1.27.1@sha256:e0174e51e81218523251d85d248a90d24c3d5e81543b4f07a5d66229397db190 AS builder

WORKDIR /src

# Download dependencies first, so they are cached between builds
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BRANCH=unknown
ARG SOURCE_DATE_EPOCH
# Set by BuildKit from --platform, e.g. linux/arm64
ARG TARGETOS=linux
ARG TARGETARCH

RUN DATE=unknown; \
    if [ -n "${SOURCE_DATE_EPOCH}" ]; then DATE=$(date -u -d "@${SOURCE_DATE_EPOCH}" +%Y-%m-%dT%H:%M:%SZ); fi; \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -buildvcs=false \
    -ldflags "-s -w \
      -X 'github.com/smartecho-hr/hetzner_cloud_exporter/internal/buildinfo.Version=${VERSION}' \
      -X 'github.com/smartecho-hr/hetzner_cloud_exporter/internal/buildinfo.Commit=${COMMIT}' \
      -X 'github.com/smartecho-hr/hetzner_cloud_exporter/internal/buildinfo.Branch=${BRANCH}' \
      -X 'github.com/smartecho-hr/hetzner_cloud_exporter/internal/buildinfo.Date=${DATE}' \
      -X 'github.com/smartecho-hr/hetzner_cloud_exporter/internal/buildinfo.BuildUser=docker' \
      -X 'github.com/smartecho-hr/hetzner_cloud_exporter/internal/buildinfo.GoVersion=$(go env GOVERSION)' \
      -X 'github.com/smartecho-hr/hetzner_cloud_exporter/internal/buildinfo.Platform=${TARGETOS}/${TARGETARCH}'" \
    -o /out/hetzner_cloud_exporter .

FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6

# The binary is owned by root and not writable by the user running it
COPY --from=builder --chmod=0555 /out/hetzner_cloud_exporter /usr/local/bin/hetzner_cloud_exporter

# A config.yaml mounted at /app/config.yaml is read automatically
WORKDIR /app

# Numeric non-root user, so Kubernetes' runAsNonRoot can verify it
USER 65532:65532

EXPOSE 9210

# A TCP check of the listen address (flags, LISTEN_ADDRESS or config file):
# works with TLS and basic auth (--web.config.file)
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
  CMD ["/usr/local/bin/hetzner_cloud_exporter", "healthcheck"]

ENTRYPOINT ["/usr/local/bin/hetzner_cloud_exporter"]
