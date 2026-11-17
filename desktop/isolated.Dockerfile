# The desktop image (Dockerfile, the desktop-test service's) plus what an
# isolated app needs to work like an installed one: a Secret Service for its
# credential store (gnome-keyring on a D-Bus session), git and ssh for the
# daemon's repository work, and sqlite3 to copy the host's data. Used by
# `just desktop::isolated` through the desktop-isolated Compose service.
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
    && rm -rf /var/lib/apt/lists/*
