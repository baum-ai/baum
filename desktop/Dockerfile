# Linux build and check environment for the desktop app: Rust, the WebKitGTK
# and GTK development packages Tauri links against, and the Tauri CLI.
# Bookworm keeps the WebKitGTK and glibc baseline older than current
# distributions, so a Linux build from it runs on them too. The release
# workflow builds the Linux bundles in this image. jq and minisign run the
# updater signing and manifest tests (scripts/updater-manifest-test.sh).
# The image index digest pins every architecture's image.
FROM rust:1.99.1-bookworm@sha256:e12566d5abc17a0da741628782cdb504b68b8961f5bfcf677936bf3e8b32de29

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        file \
        jq \
        libayatana-appindicator3-dev \
        libgtk-3-dev \
        librsvg2-bin \
        librsvg2-dev \
        libssl-dev \
        libwebkit2gtk-4.1-dev \
        libxdo-dev \
        minisign \
        xdg-utils \
    && rm -rf /var/lib/apt/lists/*

RUN rustup component add rustfmt clippy \
    && cargo install --locked tauri-cli --version 2.12.3 \
    && rm -rf /usr/local/cargo/registry \
    && chmod -R a+rwX /usr/local/cargo /usr/local/rustup

WORKDIR /oss/desktop
