# syntax=docker/dockerfile:1

# Base images are pinned by digest; bump the tag and digest together.
ARG GO_IMAGE=golang:1.25-bookworm@sha256:3b4a11519ad929d1e1d261a12cff056f0c85b735253d7d861346b9c6f8b36437
ARG RUNTIME_IMAGE=debian:bookworm-slim@sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS go-build

ARG TARGETOS
ARG TARGETARCH
ARG GOPROXY
WORKDIR /workspace

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath -o /out/agent-platform ./cmd/agent-platform

# Builtins default to the local Linux cache. scripts/release-image.sh replaces
# this stage with the lock-verified copy through --build-context builtins=...
FROM scratch AS builtins
ARG TARGETOS
ARG TARGETARCH
COPY build/builtins/${TARGETOS}-${TARGETARCH}/ /

# The runtime keeps a full Debian userland: the Agent Bash tool needs a shell
# and coreutils, and KBX links against glibc.
FROM ${RUNTIME_IMAGE}

ARG TARGETOS
ARG TARGETARCH

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates && \
    rm -rf /var/lib/apt/lists/* && \
    mkdir -p /opt/backend /opt/bin /opt/configs /opt/runtime /opt/workspaces && \
    chown -R 10001:10001 /opt

WORKDIR /opt

COPY --from=go-build --chown=10001:10001 /out/agent-platform /opt/backend/agent-platform
# Prepared by scripts/sync-linux-builtins.sh; the image never compiles Rust.
COPY --from=builtins --chown=10001:10001 / /opt/
COPY --chown=10001:10001 configs/*.example.yml configs/*.example.pem /opt/config-templates/

ENV HOME=/opt \
    SERVER_PORT=8080 \
    AP_RUNTIME_DIR=/opt/runtime

ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=unknown
LABEL org.opencontainers.image.title="agent-platform" \
      org.opencontainers.image.description="Agent Platform runtime with bundled builtins" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${CREATED}"

USER 10001:10001
EXPOSE 8080

# /healthz checks the Go HTTP runtime and the managed KBX maintenance contract
# when a knowledge-base capability is configured.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/opt/backend/agent-platform", "healthcheck"]

ENTRYPOINT ["/opt/backend/agent-platform"]
