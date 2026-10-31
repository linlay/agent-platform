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

# Linux Compose WebApp prerequisites. This is separate from the CGO-free Go
# binary and never enters the macOS/Windows program distribution.
# Reuse the pinned Bookworm compiler image; avoid downloading a second GCC
# toolchain solely for this helper. Only the resulting artifacts ship.
FROM ${GO_IMAGE} AS webapp-build
ARG TARGETOS
ARG TARGETARCH
# Official Node 24 LTS archive; update version and SHA together. No latest tag.
ENV AP_WEBAPP_NODE_VERSION=24.22.2
ENV AP_WEBAPP_NODE_SHA256=48cc0e21e048790e54c0cf5749c2725005313d652651c40b7a21feb9697aaf75
RUN test "$TARGETOS/$TARGETARCH" = linux/amd64 && \
    command -v gcc && command -v curl && command -v xz
COPY cmd/webapp-supervisor/main.c /src/main.c
RUN mkdir -p /out/node/bin /out/libexec /tmp/node && \
    curl --fail --location --proto '=https' --tlsv1.2 --retry 3 \
      "https://nodejs.org/dist/v${AP_WEBAPP_NODE_VERSION}/node-v${AP_WEBAPP_NODE_VERSION}-linux-x64.tar.xz" \
      -o /tmp/node.tar.xz && \
    printf '%s  /tmp/node.tar.xz\n' "$AP_WEBAPP_NODE_SHA256" | sha256sum -c - && \
    tar -xJf /tmp/node.tar.xz --strip-components=1 -C /tmp/node && \
    cp /tmp/node/bin/node /out/node/bin/node && \
    cp /tmp/node/LICENSE /out/node/LICENSE && \
    sha256sum /out/node/bin/node | cut -d ' ' -f 1 > /out/node/node.sha256 && \
    gcc -std=c11 -O2 -Wall -Wextra -Werror -D_FORTIFY_SOURCE=2 \
      -fstack-protector-strong -Wl,-z,relro,-z,now /src/main.c -o /out/libexec/webapp-supervisor && \
    sha256sum /out/libexec/webapp-supervisor | cut -d ' ' -f 1 > /out/libexec/webapp-supervisor.sha256

# The runtime keeps a full Debian userland: the Agent Bash tool needs a shell
# and coreutils, and KBX links against glibc.
FROM ${RUNTIME_IMAGE}

ARG TARGETOS
ARG TARGETARCH

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates libstdc++6 && \
    rm -rf /var/lib/apt/lists/* && \
    mkdir -p /opt/backend /opt/bin /opt/configs /opt/runtime /opt/workspaces && \
    chown 10001:10001 /opt/runtime /opt/workspaces

WORKDIR /opt

COPY --from=go-build --chown=10001:10001 /out/agent-platform /opt/backend/agent-platform
# Prepared by scripts/sync-linux-builtins.sh; the image never compiles Rust.
COPY --from=builtins --chown=10001:10001 / /opt/
COPY --chown=10001:10001 configs/*.example.yml configs/*.example.pem /opt/config-templates/
COPY --from=webapp-build --chown=0:0 /out/node/ /opt/node/
COPY --from=webapp-build --chown=0:0 /out/libexec/ /opt/libexec/
# The parent must also be immutable: otherwise UID 10001 could rename an
# entire runtime directory even if its files were owned by root.
RUN chown root:root /opt /opt/node /opt/node/bin /opt/libexec && \
    chmod 0755 /opt /opt/node /opt/node/bin /opt/libexec

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
