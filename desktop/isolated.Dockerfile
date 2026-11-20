# The desktop image (Dockerfile, the desktop-test service's) plus what an
# isolated app needs to work like an installed one: a Secret Service for its
# credential store (gnome-keyring on a D-Bus session), git and ssh for the
# daemon's repository work, and sqlite3 to copy the host's data. Used by
# `just desktop::isolated` through the desktop-isolated Compose service.
# WebKitWebDriver (webkit2gtk-driver, the same WebKitGTK as the app's) and
# Node.js run `just desktop::profile`'s scenarios (../app/scripts/profile).
FROM node:24.22.1-bookworm@sha256:d862bb5d082c5ae0210ac07e8c0344cb8cf7bef6d78e9b8c50c179da923108ea AS node

FROM desktop

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        dbus \
        git \
        gnome-keyring \
        libsecret-tools \
        openssh-client \
        sqlite3 \
        webkit2gtk-driver \
        weston \
    && rm -rf /var/lib/apt/lists/*
COPY --from=node /usr/local/bin/node /usr/local/bin/node
