# Environment of the agent version matrix (oss/scripts/agent-matrix/README.md):
# Go, to build baum and run the checks, and Node, to install each exact
# release of the Claude Code and Codex npm packages. bubblewrap lets Codex's
# read-only sandbox, which baum requires, start where the kernel allows it.
FROM golang:1.26.10-bookworm

COPY --from=node:24-bookworm-slim /usr/local/bin/node /usr/local/bin/node
COPY --from=node:24-bookworm-slim /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && apt-get update \
    && apt-get install -y --no-install-recommends bubblewrap ca-certificates git jq \
    && rm -rf /var/lib/apt/lists/*
