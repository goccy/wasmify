# wasmify build environment.
#
# This image bundles every tool a wasmify-managed project needs to go
# from a checked-out submodule to a wasm artefact + Go bindings, so CI
# (and `lima nerdctl run …` locally) can invoke `wasmify <subcommand>`
# without downloading wasi-sdk / binaryen / bazelisk on every run.
#
# Layer composition:
#   1. Ubuntu 24.04 + native build deps (git, python3, build-essential, etc.)
#   2. Go toolchain (copied from the official golang image so we pick up
#      the runtime files cleanly without apt-managed Go versioning).
#   3. bazelisk pinned (project-level bazel version is read from
#      .bazelversion at runtime).
#   4. buf CLI (project consumers run `buf generate` against
#      protoc-gen-wasmify-go).
#   5. wasi-sdk + binaryen pre-extracted under
#      /root/.config/wasmify/bin/{wasi-sdk,binaryen} — exactly the path
#      `internal/wasmbuild/install.go::DetectOrInstallWasiSDK` and
#      `internal/binaryen/install.go::DetectOrInstall` look at, so the
#      runtime install code becomes a no-op.
#   6. wasmify CLI + protoc-gen-wasmify-go built from the repo source.

FROM golang:1.25-bookworm AS gobuild
WORKDIR /src
COPY . .
ENV CGO_ENABLED=0
RUN go install ./cmd/wasmify ./protoc-plugins/protoc-gen-wasmify-go \
    && mkdir -p /out \
    && cp "$(go env GOPATH)/bin/wasmify"               /out/ \
    && cp "$(go env GOPATH)/bin/protoc-gen-wasmify-go" /out/

FROM ubuntu:24.04

# OS deps. python3 is needed for several bazel rule actions in
# googlesql; build-essential covers the wrapper-mode invocations that
# capture the native build before transformation. libncurses6 is a
# runtime dep of the wasi-sdk clang binary (it dynamic-links against
# the terminal library for diagnostics).
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        bash \
        build-essential \
        ca-certificates \
        clang \
        curl \
        git \
        gnupg \
        libncurses6 \
        libtool \
        make \
        ninja-build \
        openjdk-17-jdk-headless \
        pkg-config \
        python3 \
        python3-pip \
        unzip \
        wget \
        xz-utils \
        zip \
    && rm -rf /var/lib/apt/lists/*

# Go toolchain. Copy the official image's tree wholesale so version
# bumps mean changing one tag here, not maintaining an apt PPA.
COPY --from=golang:1.25-bookworm /usr/local/go /usr/local/go
ENV GOROOT=/usr/local/go \
    GOPATH=/root/go \
    PATH=/usr/local/go/bin:/root/go/bin:/usr/local/bin:/usr/bin:/bin

# Resolve the running container's CPU arch (uname -m) to each
# upstream's asset-naming convention. Centralised so individual
# install steps stay one-line. Fixed at image-build time, so this
# image is single-arch — multi-arch needs `docker buildx build
# --platform`.
RUN set -eu; case "$(uname -m)" in \
      x86_64)        bz=amd64;  wa=x86_64;  bn=x86_64;  bf=x86_64  ;; \
      aarch64|arm64) bz=arm64;  wa=arm64;   bn=aarch64; bf=aarch64 ;; \
      *) echo "unsupported arch $(uname -m)"; exit 1 ;; \
    esac; \
    echo "$bz" > /tmp/.bazelisk_arch; \
    echo "$wa" > /tmp/.wasi_arch;     \
    echo "$bn" > /tmp/.binaryen_arch; \
    echo "$bf" > /tmp/.buf_arch

# bazelisk — let it pick the bazel version from .bazelversion at run
# time so this image is project-agnostic.
ARG BAZELISK_VERSION=v1.20.0
RUN curl -fsSL -o /usr/local/bin/bazel \
      "https://github.com/bazelbuild/bazelisk/releases/download/${BAZELISK_VERSION}/bazelisk-linux-$(cat /tmp/.bazelisk_arch)" \
    && chmod +x /usr/local/bin/bazel \
    && ln -s /usr/local/bin/bazel /usr/local/bin/bazelisk

# buf — pinned major; CI consumers pin further via buf.lock.
ARG BUF_VERSION=v1.50.0
RUN curl -fsSL -o /usr/local/bin/buf \
      "https://github.com/bufbuild/buf/releases/download/${BUF_VERSION}/buf-Linux-$(cat /tmp/.buf_arch)" \
    && chmod +x /usr/local/bin/buf

# Pre-install wasi-sdk under the exact path wasmify's auto-installer
# probes (~/.config/wasmify/bin/wasi-sdk). Version must match
# `internal/wasmbuild/install.go::wasiSDKMinorVer`.
ARG WASI_SDK_VERSION=31
ARG WASI_SDK_MINOR=31.0
ENV XDG_CONFIG_HOME=/root/.config
RUN mkdir -p "${XDG_CONFIG_HOME}/wasmify/bin/wasi-sdk" \
    && curl -fsSL -o /tmp/wasi-sdk.tar.gz \
       "https://github.com/WebAssembly/wasi-sdk/releases/download/wasi-sdk-${WASI_SDK_VERSION}/wasi-sdk-${WASI_SDK_MINOR}-$(cat /tmp/.wasi_arch)-linux.tar.gz" \
    && tar -xzf /tmp/wasi-sdk.tar.gz -C "${XDG_CONFIG_HOME}/wasmify/bin/wasi-sdk" --strip-components=1 \
    && rm /tmp/wasi-sdk.tar.gz

# wasi-sdk doesn't ship a standalone `libunwind.a` for wasm32-wasip1
# (unwinding lives in the libc++ / libc++abi pair compiled with
# -mllvm -wasm-enable-sjlj). Bazel toolchains on Linux still emit
# `-l:libunwind.a` in the link command for cc_library transitive deps,
# which makes wasm-ld error out with "unable to find library". Drop an
# empty archive in the wasi-sysroot lib dir so the linker resolves the
# reference; --allow-undefined keeps any genuinely missing unwinder
# symbols as imports the host wasm runtime can satisfy.
RUN set -eu; \
    SDK="${XDG_CONFIG_HOME}/wasmify/bin/wasi-sdk"; \
    SYSROOT="${SDK}/share/wasi-sysroot"; \
    LIB="${SYSROOT}/lib/wasm32-wasip1"; \
    : > /tmp/libunwind_stub.c; \
    "${SDK}/bin/clang" --target=wasm32-wasip1 --sysroot="${SYSROOT}" \
        -c /tmp/libunwind_stub.c -o /tmp/libunwind_stub.o; \
    "${SDK}/bin/llvm-ar" rcs "${LIB}/libunwind.a" /tmp/libunwind_stub.o; \
    rm /tmp/libunwind_stub.c /tmp/libunwind_stub.o

# Pre-install binaryen under the path `internal/binaryen/install.go`
# probes. Version must match `binaryen.Version`.
ARG BINARYEN_VERSION=version_119
RUN mkdir -p "${XDG_CONFIG_HOME}/wasmify/bin/binaryen" \
    && curl -fsSL -o /tmp/binaryen.tar.gz \
       "https://github.com/WebAssembly/binaryen/releases/download/${BINARYEN_VERSION}/binaryen-${BINARYEN_VERSION}-$(cat /tmp/.binaryen_arch)-linux.tar.gz" \
    && tar -xzf /tmp/binaryen.tar.gz -C "${XDG_CONFIG_HOME}/wasmify/bin/binaryen" --strip-components=1 \
    && echo -n "${BINARYEN_VERSION}" > "${XDG_CONFIG_HOME}/wasmify/bin/binaryen/.wasmify-version" \
    && rm /tmp/binaryen.tar.gz

# wasmify CLI binaries built in stage 1 (cross-compiled for $TARGETARCH).
COPY --from=gobuild /out/wasmify                /usr/local/bin/wasmify
COPY --from=gobuild /out/protoc-gen-wasmify-go  /usr/local/bin/protoc-gen-wasmify-go

# Default to non-interactive so pipelines that miss a declarative skip
# rule fail loudly instead of hanging on a stdin prompt.
ENV WASMIFY_NON_INTERACTIVE=1

WORKDIR /work
CMD ["bash"]
