# Linux build and check environment for the desktop app: Rust, the WebKitGTK
# and GTK development packages Tauri links against, and the Tauri CLI.
# Ubuntu 24.04 is the oldest distribution the app supports. The AppImage
# carries this system's WebKitGTK, GTK and Pango but uses the host's HarfBuzz,
# glibc and other libraries linuxdeploy leaves out. The release workflow
# builds the Linux bundles in this image.
# jq and minisign run the updater signing and manifest tests
# (scripts/updater-manifest-test.sh); the image installs the release's pinned
# minisign binary. Image index digests pin every architecture's image.
FROM rust:1.99.1-bookworm@sha256:e12566d5abc17a0da741628782cdb504b68b8961f5bfcf677936bf3e8b32de29 AS rust

FROM ubuntu:24.04@sha256:2198e4c65076e9cb0b3d695219df667c0e3b0072da64ca3c2648aca17dcecaf5

# WebKit's NetworkLoadClient lifetime fix (b99f8c3) ships in 2.50.5 and newer.
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
    && pkg-config --atleast-version=2.50.5 webkit2gtk-4.1 \
    && pkg-config --modversion webkit2gtk-4.1 \
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
