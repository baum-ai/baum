# The desktop image (Dockerfile, the desktop-test service's) plus what an
# isolated app needs to work like an installed one: a Secret Service for its
# credential store (gnome-keyring on a D-Bus session), git and ssh for the
# daemon's repository work, and sqlite3 to copy the host's data. Used by
# `just desktop::isolated` through the desktop-isolated Compose service.
# WebKitWebDriver (webkit2gtk-driver, pinned to the app's WebKitGTK version) and
# Node.js run `just desktop::profile`'s scenarios (../app/scripts/profile).
# Playwright's Chromium (Chrome for Testing) and the chromedriver of the same
# version run them with --engine chromium, which records CPU profiles; both
# follow the Playwright version ../app's pnpm-lock.yaml pins, which
# ../app/scripts/profile/chromium.test.ts checks. Chrome for Testing has
# chromedriver for x86-64 Linux only, so other machines build without them.
FROM node:24.22.1-bookworm@sha256:d862bb5d082c5ae0210ac07e8c0344cb8cf7bef6d78e9b8c50c179da923108ea AS node

FROM node AS chromium
ARG PLAYWRIGHT_VERSION=1.62.0
ARG CHROMIUM_VERSION=151.0.7922.34
ARG CHROMEDRIVER_SHA256=3c2b6780156f44f3a26db5bee4a3b193d608eda55644de5287e6edf4aef2f4ac
ENV PLAYWRIGHT_BROWSERS_PATH=/opt/playwright npm_config_cache=/tmp/npm-cache
RUN mkdir -p /opt/playwright /opt/chromium \
    && if [ "$(dpkg --print-architecture)" = amd64 ]; then \
        npx -y "playwright-core@$PLAYWRIGHT_VERSION" install --no-shell chromium \
        && curl -fsSL -o /tmp/chromedriver.zip \
            "https://storage.googleapis.com/chrome-for-testing-public/$CHROMIUM_VERSION/linux64/chromedriver-linux64.zip" \
        && echo "$CHROMEDRIVER_SHA256  /tmp/chromedriver.zip" | sha256sum -c - \
        && unzip -q -j /tmp/chromedriver.zip chromedriver-linux64/chromedriver -d /opt/chromium \
        && ln -s /opt/playwright/chromium-*/chrome-linux64/chrome /opt/chromium/chrome \
        && rm -rf /tmp/chromedriver.zip /tmp/npm-cache; \
    fi

FROM desktop

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        dbus \
        git \
        gnome-keyring \
        libsecret-tools \
        openssh-client \
        sqlite3 \
        weston \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        fonts-liberation \
        libasound2t64 \
        libgbm1 \
        libnspr4 \
        libnss3 \
        libxcomposite1 \
        libxdamage1 \
        libxrandr2 \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        "webkit2gtk-driver=$(dpkg-query -W -f='${Version}' libwebkit2gtk-4.1-0)" \
    && rm -rf /var/lib/apt/lists/*
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=chromium /opt/playwright /opt/playwright
COPY --from=chromium /opt/chromium /opt/chromium
# Fails the build when Chromium lacks a library the lines above should add.
RUN [ ! -e /opt/chromium/chromedriver ] || { /opt/chromium/chrome --version && /opt/chromium/chromedriver --version; }
