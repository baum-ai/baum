# The Electron desktop image (Dockerfile, the desktop-electron service's) plus
# what an isolated app needs to work like an installed one: a Secret Service
# for its credential store (gnome-keyring on a D-Bus session), git and ssh for
# the daemon's repository work, and sqlite3 to copy the host's data. Used by
# `just desktop::isolated` through the desktop-isolated Compose service. The
# base image already has Xvfb, which --headless draws on; ImageMagick's
# `import` takes `just desktop::isolated-screenshot`'s picture of it.
# Electron's own chromedriver, of the Electron version package.json pins
# (../app/scripts/profile/electron.test.ts checks), launches and drives the
# app for `just desktop::profile` (../app/scripts/profile). Electron publishes
# it for x86-64 and arm64 Linux; the checksums are its release's.
FROM desktop

ARG ELECTRON_VERSION=45.5.0
ARG CHROMEDRIVER_SHA256_AMD64=25ebc3dbb6adfad7eb96b74db7538182bd6d40b404b764afb85c4c6ed5d6664a
ARG CHROMEDRIVER_SHA256_ARM64=19abc3fda102a6054bbd365534e341980baaf2cf588f60802286e5fd96060c89

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        dbus \
        git \
        gnome-keyring \
        imagemagick \
        libsecret-tools \
        openssh-client \
        sqlite3 \
        unzip \
    && rm -rf /var/lib/apt/lists/*

RUN case "$(dpkg --print-architecture)" in \
        amd64) arch=x64 sum=$CHROMEDRIVER_SHA256_AMD64 ;; \
        arm64) arch=arm64 sum=$CHROMEDRIVER_SHA256_ARM64 ;; \
        *) echo "no Electron chromedriver for $(dpkg --print-architecture)" >&2; exit 1 ;; \
    esac \
    && curl -fsSL -o /tmp/chromedriver.zip \
        "https://github.com/electron/electron/releases/download/v$ELECTRON_VERSION/chromedriver-v$ELECTRON_VERSION-linux-$arch.zip" \
    && echo "$sum  /tmp/chromedriver.zip" | sha256sum -c - \
    && mkdir -p /opt/electron-chromedriver \
    && unzip -q -j /tmp/chromedriver.zip chromedriver -d /opt/electron-chromedriver \
    && rm /tmp/chromedriver.zip \
    && /opt/electron-chromedriver/chromedriver --version
