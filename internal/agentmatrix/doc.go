// Package agentmatrix holds the agent version matrix: checks of baum against
// one installed release of Claude Code or Codex. Its only test, TestAgentMatrix,
// skips unless the environment names that release; scripts/agent-matrix/run.sh
// installs each release of the matrix and runs it, in Docker. See
// scripts/agent-matrix/README.md.
package agentmatrix
