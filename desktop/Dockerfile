# Linux build and check environment for the desktop app: Rust, the WebKitGTK
# and GTK development packages Tauri links against, and the Tauri CLI.
# Ubuntu 22.04 is the oldest distribution the app supports. The AppImage
# carries this system's GTK and Pango but uses the host's HarfBuzz, glibc and
# other libraries linuxdeploy leaves out, so it must be built against the
# oldest of them: a Debian 12 build's Pango needs HarfBuzz 4, which Ubuntu
# 22.04 lacks. The release workflow builds the Linux bundles in this image.
# jq and minisign run the updater signing and manifest tests
# (scripts/updater-manifest-test.sh); Ubuntu 22.04 has no minisign package,
# so the image installs the release's pinned binary. Image index digests pin
# every architecture's image.
FROM rust:1.99.1-bookworm@sha256:e12566d5abc17a0da741628782cdb504b68b8961f5bfcf677936bf3e8b32de29 AS rust

FROM ubuntu:22.04@sha256:076e7bf82319619f28dd0aa9c02ab34b055ad37100395b4f49a2ffba04fb5e11

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        build-essential \
        ca-certificates \
        curl \
        file \
        jq \
        libayatana-appindicator3-dev \
        libgtk-3-dev \
        librsvg2-bin \
        librsvg2-dev \
        libssl-dev \
        libwebkit2gtk-4.1-dev \
        libxdo-dev \
        pkg-config \
        xdg-utils \
    && rm -rf /var/lib/apt/lists/*

# The Rust toolchain targets glibc 2.17, so the official image's runs here.
ENV RUSTUP_HOME=/usr/local/rustup \
    CARGO_HOME=/usr/local/cargo \
    PATH=/usr/local/cargo/bin:$PATH
COPY --from=rust /usr/local/rustup /usr/local/rustup
COPY --from=rust /usr/local/cargo /usr/local/cargo

COPY scripts/install-minisign.sh /tmp/install-minisign.sh
RUN bash /tmp/install-minisign.sh /usr/local/bin && rm /tmp/install-minisign.sh

RUN rustup component add rustfmt clippy \
    && cargo install --locked tauri-cli --version 2.12.3 \
    && rm -rf /usr/local/cargo/registry \
    && chmod -R a+rwX /usr/local/cargo /usr/local/rustup

WORKDIR /oss/desktop
