FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS go-build

ARG TARGETOS
ARG TARGETARCH
WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath -o /out/agent-platform ./cmd/agent-platform

FROM debian:bookworm-slim

ARG TARGETOS
ARG TARGETARCH

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates && \
    rm -rf /var/lib/apt/lists/* && \
    mkdir -p /opt/backend /opt/bin /opt/runtime && \
    chown -R 10001:10001 /opt

WORKDIR /opt

COPY --from=go-build --chown=10001:10001 /out/agent-platform /opt/backend/agent-platform
# Run scripts/sync-local-builtins.sh --target linux/<arch> before building this
# image. Docker selects that verified Linux cache and never compiles Rust.
COPY --chown=10001:10001 build/builtins/${TARGETOS}-${TARGETARCH}/bin/kbase-lance-engine /opt/bin/kbase-lance-engine
COPY --chown=10001:10001 build/builtins/${TARGETOS}-${TARGETARCH}/licenses/kbase-lance-engine /opt/licenses/kbase-lance-engine

ENV AP_KBASE_LANCE_ENGINE=/opt/bin/kbase-lance-engine \
    HOME=/opt

USER 10001:10001
EXPOSE 8080

# /healthz checks the Go HTTP runtime and, when Lance KBASE is configured,
# performs the authenticated protocol-v1 sidecar health handshake through Go.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/opt/backend/agent-platform", "healthcheck"]

ENTRYPOINT ["/opt/backend/agent-platform"]
