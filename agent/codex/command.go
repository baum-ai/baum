package codex

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/baum-ai/baum/agent"
)

// command is the argument list (before any session id and the stdin marker)
// and environment of a review or a fix. cleanup removes what it wrote for the
// run, the output schema's directory; it is never nil.
func (c *Backend) command(request agent.Request, endpoint *agent.Endpoint, fix bool, extra []string) (args, env []string, cleanup func(), err error) {
	var removals []string
	cleanup = func() {
		for _, dir := range removals {
			_ = os.RemoveAll(dir)
		}
	}
	var codexHome string
	var schemaDirectory string
	sandbox := "read-only"
	if c.Profile == Container {
		sandbox = "danger-full-access"
		// Codex refuses to start when CODEX_HOME does not exist.
		codexHome = filepath.Join(request.Directory, "home", ".codex")
		if err := os.MkdirAll(codexHome, 0700); err != nil {
			return nil, nil, cleanup, err
		}
		schemaDirectory = codexHome
		env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + filepath.Join(request.Directory, "home"),
			"CODEX_HOME=" + codexHome, "CODEX_API_KEY=" + endpoint.Key}
	} else {
		env = agent.WithUserEnv(agent.LocalEnvironment(os.Environ(), "CODEX_HOME"), c.Env)
		if endpoint != nil {
			env = append(env, "CODEX_API_KEY="+endpoint.Key)
		}
	}
	var fixConfig []string
	profile := false
	if fix {
		sandbox = "workspace-write"
		c.mu.Lock()
		profile = c.Profile == Local && runtime.GOOS != "windows" && compareVersions(cmp.Or(c.version, "0.0.0"), PermissionProfileVersion) >= 0
		c.mu.Unlock()
		if fixConfig, err = fixSandbox(request, env, profile); err != nil {
			return nil, nil, cleanup, err
		}
		if request.Scratch != "" {
			env = slices.DeleteFunc(env, func(entry string) bool { return strings.HasPrefix(entry, "TMPDIR=") })
			env = append(env, "TMPDIR="+request.Scratch)
		}
		// The fix agent's commands run with this environment too: no token,
		// and caches in the scratch directory.
		env = agent.FixEnvironment(env, request.Scratch)
	}
	// Without session options the argv is exactly the ephemeral one. A kept
	// session omits --ephemeral. `exec resume` does not accept --sandbox, so
	// the sandbox goes in as config there. The subcommand postdates the
	// version floor's release notes as far as known; a Codex too old for it
	// fails with its own usage error.
	args = []string{"exec"}
	if request.ResumeSession != "" {
		args = append(args, "resume")
	}
	args = append(args, "--json")
	if !request.KeepsSession() {
		args = append(args, "--ephemeral")
	}
	// --ignore-user-config also drops any service_tier the user configured,
	// and no argument below sets one, so every request runs at the standard
	// tier that standardSpeed records.
	args = append(args, "--ignore-user-config", "--ignore-rules",
		"--disable", "hooks", "--disable", "apps", "--disable", "multi_agent")
	switch {
	case profile:
		// The permission profile in fixConfig is the sandbox; Codex refuses a
		// sandbox mode beside it.
	case request.ResumeSession != "":
		args = append(args, "-c", "sandbox_mode="+strconv.Quote(sandbox))
	default:
		args = append(args, "--sandbox", sandbox)
	}
	args = append(args, "-c", `approval_policy="never"`, "-c", `web_search="disabled"`,
		"--skip-git-repo-check")
	args = append(args, fixConfig...)
	if request.MCP != nil {
		mcp, err := mcpConfig(request.MCP)
		if err != nil {
			return nil, nil, cleanup, err
		}
		args = append(args, mcp...)
		// Set last, so neither agent_env nor anything above replaces the token.
		env = agent.WithMCPToken(env, request.MCP)
	}
	args = append(args, extra...)
	if c.Effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+strconv.Quote(c.Effort))
	}
	// An empty Model leaves the flag out so Codex uses its configured default.
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	if endpoint != nil && endpoint.BaseURL != "" {
		encoded, err := json.Marshal(endpoint.BaseURL)
		if err != nil {
			return nil, nil, cleanup, err
		}
		args = append(args, "-c", "openai_base_url="+string(encoded))
	}
	if len(request.Schema) > 0 && !fix {
		schema, err := StrictSchema(request.Schema)
		if err != nil {
			return nil, nil, cleanup, fmt.Errorf("deriving Codex output schema: %w", err)
		}
		if schemaDirectory == "" {
			// A private directory of its own: the user's CODEX_HOME holds their
			// login and configuration and is never written to.
			if schemaDirectory, err = os.MkdirTemp("", "baum-codex-schema-"); err != nil {
				return nil, nil, cleanup, err
			}
			removals = append(removals, schemaDirectory)
		}
		schemaPath := filepath.Join(schemaDirectory, "output-schema.json")
		if err := os.WriteFile(schemaPath, schema, 0600); err != nil {
			return nil, nil, cleanup, err
		}
		args = append(args, "--output-schema", schemaPath)
	}
	return args, env, cleanup, nil
}

// converseCommand is the argument list (before any session id and the stdin
// marker) and environment of a turn of the person's own session. Unlike
// command it keeps what the person set up: no --ignore-user-config,
// --ignore-rules or disabled features, no --ephemeral, and no web search,
// service tier or effort chosen for them. Full access is
// --dangerously-bypass-approvals-and-sandbox, which exec resume accepts too.
// Auto and workspace access run in the workspace-write sandbox: --sandbox on
// a first turn, and as config on exec resume, which does not take --sandbox.
// Auto access lets the agent ask to leave the sandbox and Codex's automatic
// reviewer answer, as --approve-for-me sets up (exec resume does not take that
// flag either, so it is config on both turns). Workspace access never asks,
// since nobody could answer. baum's MCP
// server is added next to the person's own; the token reaches it through the
// environment only, set last. The environment is agent.ConverseEnvironment's,
// so the person's SSH agent, gh and Git settings work, with their agent_env.
func (c *Backend) converseCommand(request agent.Request, access agent.Access) (args, env []string, err error) {
	args = []string{"exec"}
	if request.ResumeSession != "" {
		args = append(args, "resume")
	}
	args = append(args, "--json", "--skip-git-repo-check")
	switch {
	case access == agent.AccessFull:
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	case request.ResumeSession != "":
		args = append(args, "-c", `sandbox_mode="workspace-write"`)
	default:
		args = append(args, "--sandbox", "workspace-write")
	}
	switch access {
	case agent.AccessAuto:
		args = append(args, "-c", `approval_policy="on-request"`, "-c", `approvals_reviewer="auto_review"`)
	case agent.AccessWorkspace:
		args = append(args, "-c", `approval_policy="never"`)
	}
	env = agent.WithUserEnv(agent.ConverseEnvironment(os.Environ(), "CODEX_HOME"), c.Env)
	if request.MCP != nil {
		mcp, err := mcpConfig(request.MCP)
		if err != nil {
			return nil, nil, err
		}
		args = append(args, mcp...)
		env = agent.WithMCPToken(env, request.MCP)
	}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	return args, env, nil
}

// converseBinary resolves the executable and checks its version once per
// Backend, as localBinary does. Auto and workspace access run in Codex's
// sandbox, so they need the sandbox check as well; full access runs without
// one, on machines where it cannot start too.
func (c *Backend) converseBinary(ctx context.Context, access agent.Access) (string, error) {
	if access != agent.AccessFull {
		return c.localBinary(ctx)
	}
	binary, err := exec.LookPath(cmp.Or(c.Binary, "codex"))
	if err != nil {
		return "", fmt.Errorf("finding Codex: %w; install it and sign in with codex login", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// A failed check leaves no version, so the next turn checks again, as
	// it may follow an upgrade.
	if c.version == "" {
		c.version, err = checkVersion(ctx, binary)
	}
	return binary, err
}

// PermissionProfileVersion is the first Codex whose permission profiles a fix
// uses (fixSandbox): the release they were checked with. An older Codex runs
// a fix in the workspace-write sandbox, which cannot deny reads.
const PermissionProfileVersion = "0.170.0"

// fixProfile names the permission profile a fix defines.
const fixProfile = "baum_fix"

// fixSandbox is the configuration of a fix's sandbox. Commands may write to
// the checkout (request.Directory), the scratch directory and the agent's git
// directory, and to nothing else, not /tmp or the caller's TMPDIR; they
// reach the network only with request.Network.
//
// With profile, the sandbox is a permission profile: everything readable
// ("/"), the credential paths (agent.FixCredentialPaths) unreadable ("none")
// and the three directories writable. Without it, it is the workspace-write
// sandbox, which reads everything the user can: an older Codex, or Windows.
func fixSandbox(request agent.Request, environ []string, profile bool) ([]string, error) {
	var writable []string
	for _, dir := range []string{request.Scratch, request.GitDir} {
		if dir != "" {
			writable = append(writable, dir)
		}
	}
	if !profile {
		config := []string{"-c", "sandbox_workspace_write.network_access=" + strconv.FormatBool(request.Network),
			"-c", "sandbox_workspace_write.exclude_slash_tmp=true", "-c", "sandbox_workspace_write.exclude_tmpdir_env_var=true"}
		if len(writable) > 0 {
			encoded, err := json.Marshal(writable)
			if err != nil {
				return nil, err
			}
			config = append(config, "-c", "sandbox_workspace_write.writable_roots="+string(encoded))
		}
		return config, nil
	}
	var entries []string
	entry := func(path, access string) error {
		key, err := json.Marshal(path)
		if err != nil {
			return err
		}
		entries = append(entries, string(key)+"="+strconv.Quote(access))
		return nil
	}
	if err := entry("/", "read"); err != nil {
		return nil, err
	}
	for _, path := range agent.FixCredentialPaths(agent.Home(environ), environ) {
		if err := entry(path, "none"); err != nil {
			return nil, err
		}
	}
	for _, dir := range append([]string{request.Directory}, writable...) {
		if err := entry(dir, "write"); err != nil {
			return nil, err
		}
	}
	return []string{"-c", "default_permissions=" + strconv.Quote(fixProfile),
		"-c", "permissions." + fixProfile + ".filesystem={" + strings.Join(entries, ",") + "}",
		"-c", "permissions." + fixProfile + ".network.enabled=" + strconv.FormatBool(request.Network)}, nil
}
