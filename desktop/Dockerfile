# Linux build and check environment for the Electron desktop app: Node.js and
# pnpm for the build, the unit tests and electron-builder; the tools
# electron-builder's deb and AppImage targets run (fakeroot, binutils' ar for
# the deb, file, xz); the shared libraries Electron's Chromium needs and Xvfb,
# so the app and its Playwright harnesses run without a display of the host's;
# jq and the release's pinned minisign for the updater signing and manifest
# tests (scripts/updater-manifest-test.sh). Ubuntu 24.04 is the oldest
# distribution the app supports, and the release workflow builds the Linux
# bundles in this image. Image index digests pin every architecture's image.
FROM node:24.22.1-bookworm@sha256:d862bb5d082c5ae0210ac07e8c0344cb8cf7bef6d78e9b8c50c179da923108ea AS node

FROM ubuntu:24.04@sha256:2198e4c65076e9cb0b3d695219df667c0e3b0072da64ca3c2648aca17dcecaf5

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        binutils \
        ca-certificates \
        curl \
        dbus-x11 \
        fakeroot \
        file \
        jq \
        libasound2t64 \
        libatk-bridge2.0-0t64 \
        libatk1.0-0t64 \
        libcups2t64 \
        libdrm2 \
        libgbm1 \
        libgtk-3-0t64 \
        libnotify4 \
        libnss3 \
        libsecret-1-0 \
        libxkbcommon0 \
        libxss1 \
        libxtst6 \
        procps \
        xauth \
        xdg-utils \
        xvfb \
        xz-utils \
    && rm -rf /var/lib/apt/lists/*

# Node.js from the official image: its binaries need only glibc, which
# Ubuntu 24.04's is newer than. pnpm comes through corepack, pinned.
ARG PNPM_VERSION=11.0.8
ENV COREPACK_HOME=/opt/corepack \
    COREPACK_DEFAULT_TO_LATEST=0 \
    COREPACK_ENABLE_DOWNLOAD_PROMPT=0 \
    npm_config_cache=/tmp/npm-cache \
    ELECTRON_CACHE=/cache/electron \
    electron_config_cache=/cache/electron \
    ELECTRON_BUILDER_CACHE=/cache/electron-builder
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx \
    && ln -s ../lib/node_modules/corepack/dist/corepack.js /usr/local/bin/corepack \
    && corepack enable \
    && corepack install --global "pnpm@$PNPM_VERSION" \
    && chmod -R a+rwX /opt/corepack \
    && mkdir -p /cache/electron /cache/electron-builder \
    && chmod a+rwX /cache/electron /cache/electron-builder \
    && node --version && pnpm --version

COPY scripts/install-minisign.sh /tmp/install-minisign.sh
RUN bash /tmp/install-minisign.sh /usr/local/bin && rm /tmp/install-minisign.sh

# The dependency volume mounted here starts as a copy of this directory, so
# the container's user can write to it.
RUN mkdir -p /oss/desktop/node_modules && chmod a+rwX /oss/desktop/node_modules

WORKDIR /oss/desktop
