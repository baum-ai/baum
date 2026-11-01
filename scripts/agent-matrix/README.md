# Agent version matrix

Checks baum against several releases of Claude Code and Codex, not only the
latest, with no agent login and no model call. It installs each exact npm
release (`@anthropic-ai/claude-code`, `@openai/codex`) into a cache and runs
`TestAgentMatrix` ([`internal/agentmatrix`](../../internal/agentmatrix)) against
it.

```sh
just agent-matrix                                   # every release in matrix.txt
just agent-matrix --agent codex                     # one agent
just agent-matrix --agent claude-code --version 2.1.248
just agent-matrix --only-latest                     # latest and the declared floor
```

The recipe runs in the `agent-matrix` Docker service (Go and Node, built from
the [`Dockerfile`](Dockerfile)), with the development CPU set and the shared
cache in `BAUM_DEV_CACHE_DIR`. Without Docker, `sh scripts/agent-matrix/run.sh`
from this directory's parent works anywhere with Go, Node, npm, git and jq.
It needs the network (npm, and github.com/baum-ai/baum for one check) and
takes about four minutes with a warm cache. It prints a table of
checks by release and the detail of each check, and fails when a release marked
`supported` fails a check. `AGENT_MATRIX_FILE` names another matrix file.

It is not part of CI: the network installs and run time make it a manual or
scheduled check. `go test ./...` skips the test without its environment.
Run it before a release, when a new Claude Code or Codex release lands and
when the matrix or a floor changes.

## The matrix

[`matrix.txt`](matrix.txt) lists, per agent: the latest release, the older
releases of the last two to three months, baum's declared floor (the
`minimumVersion` of `agent/claude` and `agent/codex`), and releases below it,
which baum must refuse with an upgrade message (`below`). Pick dated releases
with `npm view <package> time`, and update the file and the floors together.

## What it checks, per release

| Check | What it proves |
| --- | --- |
| `version` | the executable reports the requested release |
| `doctor` | `baum internal doctor` accepts a supported release and refuses one below the floor with an upgrade message |
| `sandbox` | Codex's read-only sandbox, which baum requires, starts (skipped where the container forbids user namespaces) |
| `flags` | baum's backends build the arguments of a review, a review keeping its session, a resume, a fork, a fix and a completion, and the release's own argument parser accepts each list, run with no login and no network. A release below the floor must be refused by baum instead |
| `events` | every hook event baum registers for the agent appears in the installed package, so the hook can fire |
| `plugin` | the plugin installs from this checkout through the agent's native commands, is read back installed and enabled by baum, uninstalls, and leaves the user's own configuration intact (what the agent adds is listed) |
| `trust` | Codex loads every baum hook and reports them untrusted until reviewed (`hooks/list`, offline); Claude Code has no trust step |
| `baum-install` | `baum hooks install`, `status` and `uninstall --user` against the public marketplace, as setup step 3 and Settings run them; skipped when GitHub is unreachable |
| `replay` | hook payloads in each agent's documented shape, sent through the plugin's own dispatcher commands to a daemon on an isolated home, move a live session through start, prompt, waiting, tool, stop and end, and its transcript is served |

Each check runs on a fresh home directory. Limits: the argument check proves
the parser accepts baum's arguments, not that a model run behaves the same;
`events` finds names in the package, not that the agent fires them with the
shape replayed; `replay` uses one fixture transcript and payload shape for
every release, so it does not catch a transcript schema
change; and the doctor and sandbox checks of Codex need a host
that allows user namespaces. What needs a logged-in agent (session capture,
real reviews, fixes and chat, approvals, plan usage and Focus) needs a manual
test by someone logged in.
