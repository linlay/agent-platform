FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS go-build

ARG TARGETOS
ARG TARGETARCH
WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath -o /out/agent-platform ./cmd/agent-platform

FROM rust:1.91-bookworm AS sidecar-build

WORKDIR /workspace/native/kbase-lance-engine
RUN apt-get update && \
    apt-get install -y --no-install-recommends protobuf-compiler && \
    rm -rf /var/lib/apt/lists/*
COPY native/kbase-lance-engine/Cargo.toml native/kbase-lance-engine/Cargo.lock ./
COPY native/kbase-lance-engine/src ./src
RUN cargo build --release --locked && \
    cp target/release/kbase-lance-engine /tmp/kbase-lance-engine

FROM debian:bookworm-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates && \
    rm -rf /var/lib/apt/lists/* && \
    mkdir -p /opt/backend /opt/bin /opt/runtime && \
    chown -R 10001:10001 /opt

WORKDIR /opt

COPY --from=go-build --chown=10001:10001 /out/agent-platform /opt/backend/agent-platform
COPY --from=sidecar-build --chown=10001:10001 /tmp/kbase-lance-engine /opt/bin/kbase-lance-engine
COPY --chown=10001:10001 scripts/release-assets/licenses/kbase-lance-engine /opt/licenses/kbase-lance-engine

ENV AP_KBASE_LANCE_ENGINE=/opt/bin/kbase-lance-engine \
    HOME=/opt

USER 10001:10001
EXPOSE 8080

# /healthz checks the Go HTTP runtime and, when Lance KBASE is configured,
# performs the authenticated protocol-v1 sidecar health handshake through Go.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/opt/backend/agent-platform", "healthcheck"]

ENTRYPOINT ["/opt/backend/agent-platform"]
