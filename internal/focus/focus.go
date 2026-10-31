// Package focus brings a coding-agent session forward: the window it runs in,
// and where it can tell, its tab, its tmux pane or its Claude desktop
// conversation.
//
// A session's hooks record where they ran (Capture, sent as a hook event's
// hookevents.Terminal). Focuser takes that record and, in order:
//
//  1. opens a Claude desktop session on its own conversation, through the
//     app's claude:// link;
//  2. selects a tmux session's pane on the client most recently used for it,
//     and goes on with the terminal that client runs in;
//  3. selects the terminal's tab: kitty through its remote control, iTerm2
//     and Terminal through AppleScript, by the tab's terminal device;
//  4. raises the window: hyprctl on Hyprland, xdotool or wmctrl on X11, the
//     application bundle on macOS.
//
// It supports Linux and macOS. Other Wayland compositors than Hyprland have no
// way for one program to raise another's window, so Predict offers nothing
// there.
package focus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/hookevents"
)

// Precisions of a focus.
const (
	// Session is the session's own pane, tab or conversation.
	Session = "session"
	// Window is the window or application it runs in.
	Window = "window"
)

// Result is what a focus did.
type Result struct {
	// Precision is Session or Window, or empty when nothing was brought
	// forward.
	Precision string
	// Message says what could not be done and how to make it possible, empty
	// when everything was.
	Message string
}

// Runner runs a command with env added to the daemon's environment and returns
// its standard output.
type Runner func(ctx context.Context, env []string, name string, args ...string) ([]byte, error)

// commandTimeout bounds each command a focus runs.
const commandTimeout = 5 * time.Second

func runCommand(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.Output()
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok && len(exitErr.Stderr) > 0 {
		return out, fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(exitErr.Stderr)))
	}
	return out, err
}

// Focuser brings sessions forward. The zero value works on the system it runs
// on; the fields are seams for tests.
type Focuser struct {
	Run  Runner
	GOOS string
	// Getenv reads the daemon's environment.
	Getenv func(string) string
	// Process reads a running process: itself, its parent's id and whether
	// it runs.
	Process func(pid int) (hookevents.Process, int, bool)
	// Executable is a running process's executable file.
	Executable func(pid int) string
	// LookPath finds a command.
	LookPath func(name string) (string, error)
	// ClaudeDir is Claude desktop's configuration directory; empty is its
	// default for the platform.
	ClaudeDir string
}

func (f *Focuser) run(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	if f.Run != nil {
		return f.Run(ctx, env, name, args...)
	}
	return runCommand(ctx, env, name, args...)
}

func (f *Focuser) goos() string {
	if f.GOOS != "" {
		return f.GOOS
	}
	return runtime.GOOS
}

func (f *Focuser) getenv(name string) string {
	if f.Getenv != nil {
		return f.Getenv(name)
	}
	return os.Getenv(name)
}

func (f *Focuser) process(pid int) (hookevents.Process, int, bool) {
	if f.Process != nil {
		return f.Process(pid)
	}
	return readProcess(pid)
}

func (f *Focuser) executable(pid int) string {
	if f.Executable != nil {
		return f.Executable(pid)
	}
	return executable(pid)
}

func (f *Focuser) lookPath(name string) (string, error) {
	if f.LookPath != nil {
		return f.LookPath(name)
	}
	return exec.LookPath(name)
}

func (f *Focuser) supported() bool {
	switch f.goos() {
	case "linux", "darwin":
		return true
	}
	return false
}

// chain is pid and its ancestors, pid first, as they run now.
func (f *Focuser) chain(pid int) []hookevents.Process {
	var out []hookevents.Process
	for pid > 1 && len(out) < maxAncestors {
		p, parent, ok := f.process(pid)
		if !ok {
			break
		}
		out = append(out, p)
		pid = parent
	}
	return out
}

// living is the part of a recorded chain that still runs: from its first
// process that runs, past the shells that started the hook and exited with it,
// up to the next that exited, or whose id another process took since.
func (f *Focuser) living(recorded []hookevents.Process) []hookevents.Process {
	runs := func(p hookevents.Process) bool {
		now, _, ok := f.process(p.PID)
		return ok && now.Start == p.Start
	}
	first := slices.IndexFunc(recorded, runs)
	if first < 0 {
		return nil
	}
	chain := recorded[first:]
	for i, p := range chain {
		if !runs(p) {
			return chain[:i]
		}
	}
	return chain
}

// Predict is how precisely Focus could bring the session forward, judged from
// its record without running anything: Session, Window, or empty when it
// could not at all. The application names its action after it.
func (f *Focuser) Predict(agent string, t *hookevents.Terminal) string {
	if !f.supported() || t == nil || len(t.Processes) == 0 {
		return ""
	}
	env := f.environment(t.Env)
	if f.goos() == "linux" && !hyprland(env) && !x11(env) {
		return ""
	}
	if tmuxTerminalRecorded(t) || sharedCodex(agent, t) {
		return Session
	}
	for _, p := range t.Processes {
		switch {
		case agent == "claude-code" && claudeDesktopProcess(p.Name):
			return Session
		case p.Name == "kitty" && t.Env["KITTY_LISTEN_ON"] != "" && t.Env["KITTY_PID"] == strconv.Itoa(p.PID):
			return Session
		case f.goos() == "darwin" && t.TTY != "" && (p.Name == "Terminal" || iTermProcess(p.Name)):
			return Session
		}
	}
	return Window
}

// Focus brings the session agent/id, whose hooks recorded t, forward.
func (f *Focuser) Focus(ctx context.Context, agent, id string, t *hookevents.Terminal) Result {
	if !f.supported() {
		return Result{Message: "Bringing a session forward works on macOS and Linux only."}
	}
	if t == nil || len(t.Processes) == 0 {
		return Result{Message: "This session's hooks did not say where it runs. Sessions started before this version of baum cannot be brought forward."}
	}
	chain := f.living(t.Processes)
	if len(chain) == 0 {
		return Result{Message: "The session's process has exited."}
	}
	if sharedCodex(agent, t) {
		var located *hookevents.Terminal
		if t.Env["TMUX"] != "" {
			var why string
			located, why = f.codexTmuxPane(ctx, id, t)
			if why == codexTmuxAmbiguous {
				return Result{Message: why}
			}
		}
		if located == nil {
			return f.codexNative(ctx, id, t)
		}
		t = located
	}
	env := f.environment(t.Env)
	if agent == "claude-code" && slices.ContainsFunc(chain, func(p hookevents.Process) bool { return claudeDesktopProcess(p.Name) }) {
		if result, ok := f.claudeDesktop(ctx, id, chain, env); ok {
			return result
		}
	}
	outer, tty := chain, t.TTY
	pane := false
	if tmuxTerminalRecorded(t) {
		client, inPane, why := f.tmux(ctx, t.Env["TMUX"], t.Env["TMUX_PANE"], t.TTY)
		switch {
		case why != "":
			return Result{Message: why}
		case inPane:
			pane = true
			outer, tty = f.chain(client.pid), client.tty
		}
	}
	tab := f.selectTab(ctx, outer, tty, t.Env, pane)
	raised, why := tab.activated, ""
	if !raised {
		raised, why = f.raise(ctx, outer, env)
	}
	message := strings.Join(slices.DeleteFunc([]string{tab.hint, why}, func(s string) bool { return s == "" }), " ")
	switch {
	case (pane || tab.selected) && raised:
		return Result{Precision: Session, Message: message}
	case raised:
		return Result{Precision: Window, Message: message}
	case pane || tab.selected:
		return Result{Precision: Session, Message: "Selected the session, but could not bring its window forward. " + message}
	}
	return Result{Message: cmpOr(message, "Could not find the window this session runs in.")}
}

func cmpOr(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

// Shared Codex app servers run hooks separately from the TUI clients. Their
// inherited terminal variables locate the server's launcher, not the thread.
func sharedCodex(agent string, t *hookevents.Terminal) bool {
	return agent == "codex" && t.TTY == "" && len(t.Processes) > 0 &&
		!slices.ContainsFunc(t.Processes, func(p hookevents.Process) bool {
			return p.Name != "codex" && p.Name != "systemd" && p.Name != "launchd"
		})
}

func tmuxTerminalRecorded(t *hookevents.Terminal) bool {
	return t.TTY != "" && t.Env["TMUX"] != "" && t.Env["TMUX_PANE"] != ""
}

// Codex can include the thread ID as the final part of its terminal title.
// Long titles shorten the UUID with an ellipsis; require at least 24 characters
// and one matching pane rather than guessing from a conversation's name.
func (f *Focuser) codexTmuxPane(ctx context.Context, id string, t *hookevents.Terminal) (*hookevents.Terminal, string) {
	socket, _, _ := strings.Cut(t.Env["TMUX"], ",")
	out, err := f.run(ctx, nil, "tmux", "-S", socket, "list-panes", "-a", "-F", "#{pane_id}\t#{pane_tty}\t#{pane_current_command}\t#{pane_title}")
	if err != nil {
		return nil, fmt.Sprintf("Could not find Codex's terminal in tmux: %v.", err)
	}
	var pane, tty string
	for line := range strings.SplitSeq(string(out), "\n") {
		fields := strings.SplitN(line, "\t", 4)
		if len(fields) != 4 || fields[0] == "" || fields[1] == "" || fields[2] != "codex" {
			continue
		}
		if !codexTitleMatches(fields[3], id) {
			continue
		}
		if pane != "" {
			return nil, codexTmuxAmbiguous
		}
		pane, tty = fields[0], fields[1]
	}
	if pane == "" {
		return nil, "Could not find this Codex session's tmux pane. Open the session in Codex and include thread-id in its tui.terminal_title setting."
	}
	located := *t
	located.Env = maps.Clone(t.Env)
	located.Env["TMUX_PANE"], located.TTY = pane, tty
	return &located, ""
}

// displayEnv are the variables the window commands read, taken from the
// session's record over the daemon's own: the daemon may have been started
// from another session.
var displayEnv = []string{"DISPLAY", "WAYLAND_DISPLAY", "XDG_SESSION_TYPE", "HYPRLAND_INSTANCE_SIGNATURE", "XDG_RUNTIME_DIR"}

// environment is the display variables a session's window commands run with.
func (f *Focuser) environment(recorded map[string]string) map[string]string {
	env := map[string]string{}
	for _, name := range displayEnv {
		if value := recorded[name]; value != "" {
			env[name] = value
		} else if value := f.getenv(name); value != "" {
			env[name] = value
		}
	}
	return env
}

func pairs(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for _, name := range displayEnv {
		if value, ok := env[name]; ok {
			out = append(out, name+"="+value)
		}
	}
	return out
}

func hyprland(env map[string]string) bool { return env["HYPRLAND_INSTANCE_SIGNATURE"] != "" }

// x11 reports an X11 session. XWayland sets DISPLAY under Wayland too, where
// xdotool cannot raise the compositor's own windows.
func x11(env map[string]string) bool {
	return env["DISPLAY"] != "" && env["WAYLAND_DISPLAY"] == "" && env["XDG_SESSION_TYPE"] != "wayland"
}

// tmuxClient is a terminal attached to a tmux server.
type tmuxClient struct {
	pid      int
	tty      string
	session  string
	activity int64
}

// tmux selects pane on the tmux server whose $TMUX value is server: on the
// client most recently used for the pane's session, else on the one most
// recently used at all, which is switched to that session. It reports false,
// selecting nothing, when the pane's terminal is not tty, the session's: a
// terminal application started from a tmux pane passes TMUX on to the shells
// of its own windows. On failure it returns why, for the user.
func (f *Focuser) tmux(ctx context.Context, server, pane, tty string) (tmuxClient, bool, string) {
	socket, _, _ := strings.Cut(server, ",")
	tmux := func(args ...string) ([]byte, error) {
		return f.run(ctx, nil, "tmux", append([]string{"-S", socket}, args...)...)
	}
	out, err := tmux("display-message", "-p", "-t", pane, "#{session_id}\t#{pane_tty}")
	if err != nil {
		return tmuxClient{}, false, "The session's tmux pane is gone."
	}
	session, paneTTY, _ := strings.Cut(strings.TrimSpace(string(out)), "\t")
	// display-message can succeed with empty fields for a pane that is gone.
	if session == "" || paneTTY == "" {
		return tmuxClient{}, false, "The session's tmux pane is gone."
	}
	if paneTTY != tty {
		return tmuxClient{}, false, ""
	}
	out, err = tmux("list-clients", "-F", "#{client_pid}\t#{client_tty}\t#{session_id}\t#{client_activity}")
	if err != nil {
		return tmuxClient{}, false, fmt.Sprintf("Could not list the terminals attached to tmux: %v.", err)
	}
	// A client on the pane's session wins over one on another; then the one
	// used last.
	better := func(c, than tmuxClient) bool {
		if (c.session == session) != (than.session == session) {
			return c.session == session
		}
		return c.activity > than.activity
	}
	var best *tmuxClient
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 4 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		activity, _ := strconv.ParseInt(fields[3], 10, 64)
		c := tmuxClient{pid: pid, tty: fields[1], session: fields[2], activity: activity}
		if best == nil || better(c, *best) {
			best = &c
		}
	}
	if best == nil {
		return tmuxClient{}, false, "No terminal is attached to the session's tmux server. Attach to it to bring the session forward."
	}
	for _, args := range [][]string{
		{"switch-client", "-c", best.tty, "-t", session},
		{"select-window", "-t", pane},
		{"select-pane", "-t", pane},
	} {
		if _, err := tmux(args...); err != nil {
			return tmuxClient{}, false, fmt.Sprintf("Could not select the session's tmux pane: %v.", err)
		}
	}
	return *best, true, ""
}

// tabResult is what selecting a terminal's tab did.
type tabResult struct {
	selected bool
	// activated reports that the step also brought the terminal forward.
	activated bool
	hint      string
}

// selectTab selects the tab of the terminal in chain whose process is
// chain's first, or whose terminal device is tty. inTmux says chain is a tmux
// client's, so the recorded variables describe the terminal the tmux server
// started in, which may be another.
func (f *Focuser) selectTab(ctx context.Context, chain []hookevents.Process, tty string, env map[string]string, inTmux bool) tabResult {
	for i, p := range chain {
		switch {
		case p.Name == "kitty":
			return f.kittyTab(ctx, chain, i, env, inTmux)
		case f.goos() == "darwin" && iTermProcess(p.Name):
			return f.appleScriptTab(ctx, "iTerm2", iTermScript, tty)
		case f.goos() == "darwin" && p.Name == "Terminal":
			return f.appleScriptTab(ctx, "Terminal", terminalScript, tty)
		}
	}
	return tabResult{}
}

// kittyTab focuses the kitty window whose program is chain[i-1], the process
// kitty (chain[i]) started, over kitty's remote control socket. The socket is
// known only from a hook that ran in that kitty. Without it, a session outside
// tmux is told how to turn it on; in tmux, which selected the pane, kitty
// commonly shows only the one tab, so it is not.
func (f *Focuser) kittyTab(ctx context.Context, chain []hookevents.Process, i int, env map[string]string, inTmux bool) tabResult {
	kitty := chain[i]
	socket := env["KITTY_LISTEN_ON"]
	if socket == "" || env["KITTY_PID"] != strconv.Itoa(kitty.PID) {
		if inTmux {
			return tabResult{}
		}
		return tabResult{hint: "To select kitty's tab too, turn on its remote control: allow_remote_control socket-only and listen_on unix:/tmp/kitty-{kitty_pid} in kitty.conf, then start a new session."}
	}
	var match string
	switch {
	case i > 0:
		match = "pid:" + strconv.Itoa(chain[i-1].PID)
	case !inTmux && env["KITTY_WINDOW_ID"] != "":
		match = "id:" + env["KITTY_WINDOW_ID"]
	default:
		return tabResult{}
	}
	// On macOS kitten is on the PATH of kitty's own shells only; it sits
	// beside kitty in its bundle.
	kitten := "kitten"
	if _, err := f.lookPath(kitten); err != nil {
		if exe := f.executable(kitty.PID); exe != "" {
			kitten = filepath.Join(filepath.Dir(exe), "kitten")
		}
	}
	if _, err := f.run(ctx, nil, kitten, "@", "--to", socket, "focus-window", "--match", match); err != nil {
		return tabResult{hint: fmt.Sprintf("Could not select kitty's tab: %v.", err)}
	}
	return tabResult{selected: true}
}

func iTermProcess(name string) bool {
	return name == "iTerm2" || strings.HasPrefix(name, "iTermServer")
}

// iTermScript selects the iTerm2 session on the terminal device named by its
// argument and brings it forward.
var iTermScript = []string{
	`on run argv`,
	`set wanted to item 1 of argv`,
	`tell application "iTerm2"`,
	`repeat with w in windows`,
	`repeat with t in tabs of w`,
	`repeat with s in sessions of t`,
	`if tty of s is wanted then`,
	`select w`,
	`select t`,
	`select s`,
	`activate`,
	`return "found"`,
	`end if`,
	`end repeat`,
	`end repeat`,
	`end repeat`,
	`end tell`,
	`return "missing"`,
	`end run`,
}

// terminalScript does the same in Terminal.
var terminalScript = []string{
	`on run argv`,
	`set wanted to item 1 of argv`,
	`tell application "Terminal"`,
	`repeat with w in windows`,
	`repeat with t in tabs of w`,
	`if tty of t is wanted then`,
	`set selected tab of w to t`,
	`set index of w to 1`,
	`activate`,
	`return "found"`,
	`end if`,
	`end repeat`,
	`end repeat`,
	`end tell`,
	`return "missing"`,
	`end run`,
}

// appleScriptTab runs a tab script for tty with osascript.
func (f *Focuser) appleScriptTab(ctx context.Context, app string, script []string, tty string) tabResult {
	if tty == "" {
		return tabResult{}
	}
	args := make([]string, 0, 2*len(script)+1)
	for _, line := range script {
		args = append(args, "-e", line)
	}
	out, err := f.run(ctx, nil, "osascript", append(args, tty)...)
	switch {
	case err != nil && strings.Contains(err.Error(), "-1743"):
		return tabResult{hint: fmt.Sprintf("To select %s's tab, allow baum to control %s in System Settings, Privacy & Security, Automation.", app, app)}
	case err != nil:
		return tabResult{hint: fmt.Sprintf("Could not select %s's tab: %v.", app, err)}
	case strings.TrimSpace(string(out)) != "found":
		return tabResult{}
	}
	return tabResult{selected: true, activated: true}
}

// raise brings forward the window of the nearest process in chain that has
// one, or says why it could not.
func (f *Focuser) raise(ctx context.Context, chain []hookevents.Process, env map[string]string) (bool, string) {
	switch {
	case f.goos() == "darwin":
		return f.raiseApp(ctx, chain)
	case hyprland(env):
		return f.raiseHyprland(ctx, chain, env)
	case x11(env):
		return f.raiseX11(ctx, chain, env)
	}
	return false, "Bringing windows forward works on Hyprland and X11; this desktop is neither."
}

// iTermBundle is iTerm2's bundle identifier. Its shells' parent, iTermServer,
// runs from iTerm2's support folder, outside the application bundle.
const iTermBundle = "com.googlecode.iterm2"

// raiseApp activates the application whose bundle holds the executable of
// the nearest process in chain inside one, or iTerm2 for its server.
func (f *Focuser) raiseApp(ctx context.Context, chain []hookevents.Process) (bool, string) {
	for _, p := range chain {
		args := []string{"-b", iTermBundle}
		if !iTermProcess(p.Name) {
			app := appBundle(f.executable(p.PID))
			if app == "" {
				continue
			}
			args = []string{"-a", app}
		}
		if _, err := f.run(ctx, nil, "open", args...); err != nil {
			return false, fmt.Sprintf("Could not bring %s forward: %v.", filepath.Base(args[1]), err)
		}
		return true, ""
	}
	return false, "Could not find the application this session runs in."
}

// appBundle is the outermost application bundle holding exe, or "".
func appBundle(exe string) string {
	i := strings.Index(exe, ".app/")
	if i < 0 {
		return ""
	}
	return exe[:i+len(".app")]
}

// hyprClient is a window as hyprctl lists it.
type hyprClient struct {
	Address        string        `json:"address"`
	Title          string        `json:"title"`
	PID            int           `json:"pid"`
	FocusHistoryID int           `json:"focusHistoryID"`
	Workspace      hyprWorkspace `json:"workspace"`
}

type hyprWorkspace struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// hyprShowWorkspace switches to the window's workspace unless a monitor
// shows it already: focuswindow does not always leave it, as with a plugin's
// workspaces, and switching to the workspace shown would go back to the last
// one under binds:workspace_back_and_forth. A special workspace, or one
// Hyprland did not name, is left to focuswindow, as is any failure here.
func (f *Focuser) hyprShowWorkspace(ctx context.Context, vars []string, w hyprWorkspace) {
	if w.ID == 0 || w.Name == "" || strings.HasPrefix(w.Name, "special:") {
		return
	}
	out, err := f.run(ctx, vars, "hyprctl", "-j", "monitors")
	if err != nil {
		return
	}
	var monitors []struct {
		ActiveWorkspace hyprWorkspace `json:"activeWorkspace"`
	}
	if json.Unmarshal(out, &monitors) != nil {
		return
	}
	for _, m := range monitors {
		if m.ActiveWorkspace.ID == w.ID {
			return
		}
	}
	target, lua := strconv.Itoa(w.ID), strconv.Itoa(w.ID)
	if w.ID < 0 {
		target = "name:" + w.Name
		lua = strconv.Quote(target)
	}
	_, _ = f.hyprDispatch(ctx, vars, "hl.dsp.focus({ workspace = "+lua+" })", "workspace", target)
}

// hyprDispatch runs a dispatcher as `hyprctl dispatch` takes it from a
// hyprland.conf Hyprland, and again as the Lua it evaluates instead when
// Hyprland reads hyprland.lua, which says so in its answer. It answers what
// Hyprland did, `ok` when it did it.
func (f *Focuser) hyprDispatch(ctx context.Context, vars []string, lua string, dispatcher ...string) (string, error) {
	out, err := f.run(ctx, vars, "hyprctl", append([]string{"dispatch"}, dispatcher...)...)
	if answer := strings.TrimSpace(string(out)); answer == "ok" || !strings.Contains(answer, "hl.dispatch") {
		return answer, err
	}
	out, err = f.run(ctx, vars, "hyprctl", "dispatch", lua)
	return strings.TrimSpace(string(out)), err
}

func (f *Focuser) raiseHyprland(ctx context.Context, chain []hookevents.Process, env map[string]string) (bool, string) {
	vars := pairs(env)
	out, err := f.run(ctx, vars, "hyprctl", "-j", "clients")
	if err != nil {
		return false, fmt.Sprintf("Could not list Hyprland's windows: %v.", err)
	}
	var clients []hyprClient
	if err := json.Unmarshal(out, &clients); err != nil {
		return false, "Could not read Hyprland's windows."
	}
	for _, p := range chain {
		var best *hyprClient
		for i, c := range clients {
			// The lowest focus history is the window used last.
			if c.PID == p.PID && (best == nil || c.FocusHistoryID < best.FocusHistoryID) {
				best = &clients[i]
			}
		}
		if best == nil {
			continue
		}
		f.hyprShowWorkspace(ctx, vars, best.Workspace)
		window := "address:" + best.Address
		out, err := f.hyprDispatch(ctx, vars, "hl.dsp.focus({ window = "+strconv.Quote(window)+" })", "focuswindow", window)
		if err != nil || out != "ok" {
			return false, fmt.Sprintf("Hyprland did not focus the window: %s.", cmpOr(out, fmt.Sprint(err)))
		}
		return true, ""
	}
	return false, "Could not find the window this session runs in."
}

func (f *Focuser) raiseX11(ctx context.Context, chain []hookevents.Process, env map[string]string) (bool, string) {
	vars := pairs(env)
	if _, err := f.lookPath("xdotool"); err == nil {
		// Visible windows first; a minimized window is unmapped, which
		// --onlyvisible leaves out, so the chain is searched again without it.
		for _, visible := range []bool{true, false} {
			for _, p := range chain {
				args := []string{"search", "--pid", strconv.Itoa(p.PID)}
				if visible {
					args = []string{"search", "--onlyvisible", "--pid", strconv.Itoa(p.PID)}
				}
				out, err := f.run(ctx, vars, "xdotool", args...)
				if err != nil {
					continue
				}
				ids := strings.Fields(string(out))
				if len(ids) == 0 {
					continue
				}
				if _, err := f.run(ctx, vars, "xdotool", "windowactivate", ids[len(ids)-1]); err != nil {
					return false, fmt.Sprintf("Could not bring the window forward: %v.", err)
				}
				return true, ""
			}
		}
		return false, "Could not find the window this session runs in."
	}
	if _, err := f.lookPath("wmctrl"); err != nil {
		return false, "Install xdotool or wmctrl to bring windows forward on X11."
	}
	out, err := f.run(ctx, vars, "wmctrl", "-lp")
	if err != nil {
		return false, fmt.Sprintf("Could not list the windows: %v.", err)
	}
	for _, p := range chain {
		for line := range strings.SplitSeq(string(out), "\n") {
			// id desktop pid host title...
			fields := strings.Fields(line)
			if len(fields) >= 3 && fields[2] == strconv.Itoa(p.PID) {
				if _, err := f.run(ctx, vars, "wmctrl", "-i", "-a", fields[0]); err != nil {
					return false, fmt.Sprintf("Could not bring the window forward: %v.", err)
				}
				return true, ""
			}
		}
	}
	return false, "Could not find the window this session runs in."
}

// claudeDesktopProcess reports Claude desktop's main process: claude-desktop
// on Linux, Claude on macOS.
func claudeDesktopProcess(name string) bool {
	return name == "claude-desktop" || name == "Claude"
}

// claudeDesktop opens a Claude Code session that runs in Claude desktop on its
// conversation, and reports false for one that does not run there.
func (f *Focuser) claudeDesktop(ctx context.Context, id string, chain []hookevents.Process, env map[string]string) (Result, bool) {
	local := f.claudeDesktopSession(id)
	if local == "" {
		return Result{}, false
	}
	opener := "xdg-open"
	if f.goos() == "darwin" {
		opener = "open"
	}
	if _, err := f.run(ctx, pairs(env), opener, "claude://code/continue?session="+local); err != nil {
		return Result{}, false
	}
	// Claude desktop focuses its window on a link, which a Wayland
	// compositor may only mark as wanting attention.
	raised, why := f.raise(ctx, chain, env)
	if !raised && f.goos() != "darwin" {
		return Result{Precision: Session, Message: "Opened the session in Claude desktop, but could not bring its window forward. " + why}, true
	}
	return Result{Precision: Session}, true
}

// localSession is Claude desktop's id of a session it runs.
var localSession = regexp.MustCompile(`^local_[A-Za-z0-9-]{1,64}$`)

// claudeDesktopSession is Claude desktop's id (local_…) of the session whose
// Claude Code session id is id, or "" when Claude desktop has none, from the
// session records it keeps under claude-code-sessions/<account>/<org>/. These
// are its internal files: a format it changes reads as no session.
func (f *Focuser) claudeDesktopSession(id string) string {
	dir := f.ClaudeDir
	if dir == "" {
		dir = claudeConfigDir(f.goos(), f.getenv)
	}
	if dir == "" || id == "" {
		return ""
	}
	files, _ := filepath.Glob(filepath.Join(dir, "claude-code-sessions", "*", "*", "local_*.json"))
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		var record struct {
			SessionID string   `json:"sessionId"`
			CLI       string   `json:"cliSessionId"`
			Previous  []string `json:"previousCliSessionIds"`
			Archived  bool     `json:"isArchived"`
		}
		if json.Unmarshal(data, &record) != nil || record.Archived || !localSession.MatchString(record.SessionID) {
			continue
		}
		if record.CLI == id || slices.Contains(record.Previous, id) {
			return record.SessionID
		}
	}
	return ""
}

// claudeConfigDir is where Claude desktop keeps its configuration.
func claudeConfigDir(goos string, getenv func(string) string) string {
	home := getenv("HOME")
	switch goos {
	case "darwin":
		if home == "" {
			return ""
		}
		return filepath.Join(home, "Library", "Application Support", "Claude")
	case "linux":
		if config := getenv("XDG_CONFIG_HOME"); config != "" {
			return filepath.Join(config, "Claude")
		}
		if home == "" {
			return ""
		}
		return filepath.Join(home, ".config", "Claude")
	}
	return ""
}
