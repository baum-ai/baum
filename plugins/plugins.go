// Package plugins carries the manifests of the agent plugins this source tree
// publishes, so a baum build knows which plugin version it shipped with.
package plugins

import (
	_ "embed"
	"encoding/json"
)

//go:embed baum-claude-code/.claude-plugin/plugin.json
var claudeCode []byte

//go:embed baum-codex/.codex-plugin/plugin.json
var codex []byte

// Version is the plugin version this build shipped for agent ("claude-code"
// or "codex"), or "" for any other agent. The marketplace has at least this
// version; it may have a newer one.
func Version(agent string) string {
	manifest := map[string][]byte{"claude-code": claudeCode, "codex": codex}[agent]
	var m struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(manifest, &m) != nil {
		return ""
	}
	return m.Version
}
