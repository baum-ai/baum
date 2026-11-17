# The desktop build image (oss/desktop/Dockerfile) with a virtual display,
# a mouse driver and Node.js, for `just desktop::navigation-check`.
ARG DESKTOP_IMAGE=baum-desktop-build

FROM node:24.22.1-bookworm@sha256:d862bb5d082c5ae0210ac07e8c0344cb8cf7bef6d78e9b8c50c179da923108ea AS node

FROM ${DESKTOP_IMAGE}
USER root
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends xvfb xdotool dbus \
    && rm -rf /var/lib/apt/lists/*
COPY --from=node /usr/local/bin/node /usr/local/bin/node
