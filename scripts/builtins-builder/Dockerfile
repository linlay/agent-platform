# Native Linux builder for the sibling builtin collection. It only prepares
# build/builtins/linux-<arch>/ through scripts/sync-local-builtins.sh; the
# runtime image (root Dockerfile) never compiles builtins.
#
# The base matches the runtime image's Debian release so glibc-linked builtins
# (KBX) run there unchanged.
FROM golang:1.26-bookworm

ARG RUST_TOOLCHAIN=stable

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        build-essential ca-certificates clang cmake curl git \
        libprotobuf-dev pkg-config protobuf-compiler python3 rsync xz-utils && \
    rm -rf /var/lib/apt/lists/*

ENV RUSTUP_HOME=/opt/rustup \
    CARGO_HOME=/opt/cargo \
    PATH=/opt/cargo/bin:/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
RUN curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | \
    sh -s -- -y --profile minimal --default-toolchain "$RUST_TOOLCHAIN" --no-modify-path && \
    chmod -R a+rwX /opt/rustup /opt/cargo

WORKDIR /work
