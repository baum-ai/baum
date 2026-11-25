# The Electron desktop image (Dockerfile, the desktop-electron service's) plus
# the OpenGL libraries a Tauri AppImage takes from the system rather than
# bundling: EGL and Mesa's software drivers, which WebKitGTK draws with under
# Xvfb. Used by `just desktop::bridge-test` (scripts/bridge-test.sh run)
# through the desktop-bridge Compose service, which runs the Tauri AppImage
# and lets it update itself to the Electron one. Remove with the Tauri crate.
FROM desktop

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        libegl1 \
        libegl-mesa0 \
        libgl1-mesa-dri \
        libgles2 \
    && rm -rf /var/lib/apt/lists/*
