# Checks and Linux runs of the Electron desktop app (oss/desktop/electron):
# Node.js and pnpm for the build and unit tests, the shared libraries
# Electron's Chromium needs, and Xvfb so the app and its Playwright harnesses
# run without a display of the host's.
FROM node:24.22.1-bookworm@sha256:d862bb5d082c5ae0210ac07e8c0344cb8cf7bef6d78e9b8c50c179da923108ea

ARG PNPM_VERSION=11.0.8

ENV COREPACK_HOME=/opt/corepack \
    COREPACK_DEFAULT_TO_LATEST=0 \
    COREPACK_ENABLE_DOWNLOAD_PROMPT=0 \
    npm_config_cache=/tmp/npm-cache \
    ELECTRON_CACHE=/cache/electron \
    electron_config_cache=/cache/electron
RUN corepack enable \
    && corepack install --global "pnpm@$PNPM_VERSION" \
    && chmod -R a+rwX /opt/corepack

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        dbus-x11 \
        libasound2 \
        libatk-bridge2.0-0 \
        libatk1.0-0 \
        libcups2 \
        libdrm2 \
        libgbm1 \
        libgtk-3-0 \
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
    && rm -rf /var/lib/apt/lists/*

# The dependency volume mounted here starts as a copy of this directory, so
# the container's user can write to it.
RUN mkdir -p /oss/desktop/node_modules && chmod a+rwX /oss/desktop/node_modules

WORKDIR /oss/desktop
