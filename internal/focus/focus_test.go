package focus

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/hookevents"
)

// machine is a fake system: its processes and the answers of the commands a
// focus runs, which it records.
type machine struct {
	procs   map[int]proc
	exes    map[int]string
	answers map[string]answer
	paths   map[string]bool
	calls   []string
}

type proc struct {
	name   string
	start  string
	parent int
}

type answer struct {
	out string
	err error
}

func (m *machine) focuser(goos string, env map[string]string) *Focuser {
	return &Focuser{
		GOOS:   goos,
		Getenv: func(name string) string { return env[name] },
		Run: func(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
			call := strings.Join(append([]string{name}, args...), " ")
			m.calls = append(m.calls, call)
			for prefix, a := range m.answers {
				if strings.HasPrefix(call, prefix) {
					return []byte(a.out), a.err
				}
			}
			return nil, nil
		},
		Process: func(pid int) (hookevents.Process, int, bool) {
			p, ok := m.procs[pid]
			return hookevents.Process{PID: pid, Start: p.start, Name: p.name}, p.parent, ok
		},
		Executable: func(pid int) string { return m.exes[pid] },
		LookPath: func(name string) (string, error) {
			if m.paths[name] {
				return "/usr/bin/" + name, nil
			}
			return "", errors.New("not found")
		},
		ClaudeDir: "/nonexistent",
	}
}

// recorded is the chain a hook in pid would record.
func (m *machine) recorded(pid int) []hookevents.Process {
	var out []hookevents.Process
	for pid > 1 {
		p, ok := m.procs[pid]
		if !ok {
			break
		}
		out = append(out, hookevents.Process{PID: pid, Start: p.start, Name: p.name})
		pid = p.parent
	}
	return out
}

// tmuxInKitty is Linux with a Claude Code session in tmux pane %7 of session
// $1; the tmux client attached to it runs in kitty 300.
func tmuxInKitty() *machine {
	return &machine{
		procs: map[int]proc{
			100: {"claude", "a", 101},
			101: {"zsh", "b", 50},
			50:  {"tmux: server", "c", 1},
			200: {"tmux: client", "d", 300},
			201: {"tmux: client", "e", 400},
			300: {"kitty", "f", 1},
			400: {"kitty", "g", 1},
		},
		answers: map[string]answer{
			"tmux -S /tmp/tmux-1000/default display-message": {out: "$1\t/dev/pts/12\n"},
			"tmux -S /tmp/tmux-1000/default list-clients": {out: "201\t/dev/pts/9\t$2\t900\n" +
				"200\t/dev/pts/4\t$1\t500\n" +
				"202\t/dev/pts/8\t$1\t100\n"},
			"hyprctl -j clients": {out: `[{"address":"0xold","pid":300,"focusHistoryID":4},{"address":"0xother","pid":400,"focusHistoryID":0},{"address":"0xnew","pid":300,"focusHistoryID":2}]`},
			"hyprctl dispatch":   {out: "ok"},
		},
	}
}

var hyprEnv = map[string]string{"HYPRLAND_INSTANCE_SIGNATURE": "sig", "WAYLAND_DISPLAY": "wayland-1"}

func tmuxTerminal(m *machine) *hookevents.Terminal {
	return &hookevents.Terminal{
		Env:       map[string]string{"TMUX": "/tmp/tmux-1000/default,50,0", "TMUX_PANE": "%7", "KITTY_PID": "400"},
		TTY:       "/dev/pts/12",
		Processes: m.recorded(100),
	}
}

func TestTmuxPaneOnTheClientUsedForItsSession(t *testing.T) {
	m := tmuxInKitty()
	result := m.focuser("linux", hyprEnv).Focus(context.Background(), "claude-code", "s1", tmuxTerminal(m))
	if result != (Result{Precision: Session}) {
		t.Fatalf("result = %+v", result)
	}
	want := []string{
		"tmux -S /tmp/tmux-1000/default display-message -p -t %7 #{session_id}\t#{pane_tty}",
		"tmux -S /tmp/tmux-1000/default list-clients -F #{client_pid}\t#{client_tty}\t#{session_id}\t#{client_activity}",
		// The client on the pane's session used last, not the busier one
		// elsewhere.
		"tmux -S /tmp/tmux-1000/default switch-client -c /dev/pts/4 -t $1",
		"tmux -S /tmp/tmux-1000/default select-window -t %7",
		"tmux -S /tmp/tmux-1000/default select-pane -t %7",
		"hyprctl -j clients",
		// kitty 300's window used last; the recorded KITTY_PID is the one
		// the tmux server started in.
		"hyprctl dispatch focuswindow address:0xnew",
	}
	if !reflect.DeepEqual(m.calls, want) {
		t.Fatalf("calls:\n%s", strings.Join(m.calls, "\n"))
	}
}

func TestTmuxWithNoClientOnItsSessionSwitchesTheLastUsed(t *testing.T) {
	m := tmuxInKitty()
	m.answers["tmux -S /tmp/tmux-1000/default display-message"] = answer{out: "$3\t/dev/pts/12\n"}
	m.focuser("linux", hyprEnv).Focus(context.Background(), "claude-code", "s1", tmuxTerminal(m))
	if !slices.Contains(m.calls, "tmux -S /tmp/tmux-1000/default switch-client -c /dev/pts/9 -t $3") ||
		!slices.Contains(m.calls, "hyprctl dispatch focuswindow address:0xother") {
		t.Fatalf("calls:\n%s", strings.Join(m.calls, "\n"))
	}
}

func TestTmuxWithoutClients(t *testing.T) {
	m := tmuxInKitty()
	m.answers["tmux -S /tmp/tmux-1000/default list-clients"] = answer{}
	result := m.focuser("linux", hyprEnv).Focus(context.Background(), "claude-code", "s1", tmuxTerminal(m))
	if result.Precision != "" || !strings.Contains(result.Message, "No terminal is attached") {
		t.Fatalf("result = %+v", result)
	}
}

func TestExitedSession(t *testing.T) {
	m := tmuxInKitty()
	terminal := tmuxTerminal(m)
	// Every recorded process exited; 100's id went to another process.
	m.procs[100] = proc{"bash", "z", 1}
	delete(m.procs, 101)
	delete(m.procs, 50)
	result := m.focuser("linux", hyprEnv).Focus(context.Background(), "claude-code", "s1", terminal)
	if result.Precision != "" || result.Message != "The session's process has exited." || len(m.calls) != 0 {
		t.Fatalf("result = %+v, calls %v", result, m.calls)
	}
}

// The shells that started the hook exit with it; the chain starts at the
// first recorded process that still runs.
func TestTheHooksExitedShellsAreSkipped(t *testing.T) {
	m, terminal := kittyShell()
	terminal.Processes = append([]hookevents.Process{{PID: 99, Start: "s", Name: "sh"}}, terminal.Processes...)
	result := m.focuser("linux", hyprEnv).Focus(context.Background(), "codex", "s1", terminal)
	if result.Precision != Window || m.calls[len(m.calls)-1] != "hyprctl dispatch focuswindow address:0xk" {
		t.Fatalf("result = %+v, calls %v", result, m.calls)
	}
}

// A kitty started from a tmux pane passes TMUX to its own shells: a session
// there is not in that pane, whose terminal is another.
func TestTmuxVariablesOfAnotherPaneAreIgnored(t *testing.T) {
	m, terminal := kittyShell()
	m.answers["tmux -S /tmp/tmux-1000/default display-message"] = answer{out: "$1\t/dev/pts/2\n"}
	terminal.Env["TMUX"], terminal.Env["TMUX_PANE"], terminal.TTY = "/tmp/tmux-1000/default,50,0", "%7", "/dev/pts/12"
	result := m.focuser("linux", hyprEnv).Focus(context.Background(), "codex", "s1", terminal)
	if result.Precision != Window || len(m.calls) != 3 || m.calls[2] != "hyprctl dispatch focuswindow address:0xk" {
		t.Fatalf("result = %+v, calls:\n%s", result, strings.Join(m.calls, "\n"))
	}
}

// kittyShell is a Claude Code session in kitty 300's window running zsh 101.
func kittyShell() (*machine, *hookevents.Terminal) {
	m := &machine{
		procs: map[int]proc{100: {"claude", "a", 101}, 101: {"zsh", "b", 300}, 300: {"kitty", "f", 1}},
		answers: map[string]answer{
			"hyprctl -j clients": {out: `[{"address":"0xk","pid":300,"focusHistoryID":1}]`},
			"hyprctl dispatch":   {out: "ok"},
		},
	}
	return m, &hookevents.Terminal{Env: map[string]string{"KITTY_PID": "300", "KITTY_WINDOW_ID": "3"}, Processes: m.recorded(100)}
}

func TestKittyTabOverRemoteControl(t *testing.T) {
	m, terminal := kittyShell()
	terminal.Env["KITTY_LISTEN_ON"] = "unix:/tmp/kitty-300"
	result := m.focuser("linux", hyprEnv).Focus(context.Background(), "codex", "s1", terminal)
	if result != (Result{Precision: Session}) {
		t.Fatalf("result = %+v", result)
	}
	if m.calls[0] != "kitten @ --to unix:/tmp/kitty-300 focus-window --match pid:101" ||
		m.calls[len(m.calls)-1] != "hyprctl dispatch focuswindow address:0xk" {
		t.Fatalf("calls:\n%s", strings.Join(m.calls, "\n"))
	}
}

// On Hyprland the window's workspace is shown first, unless a monitor shows
// it already; a special workspace is left to focuswindow.
func TestHyprlandShowsTheWindowsWorkspace(t *testing.T) {
	cases := []struct {
		name, workspace string
		want            []string
	}{
		{"hidden", `{"id":10,"name":"2-10"}`, []string{"hyprctl -j monitors", "hyprctl dispatch workspace 10"}},
		{"named", `{"id":-98,"name":"notes"}`, []string{"hyprctl -j monitors", "hyprctl dispatch workspace name:notes"}},
		{"shown", `{"id":13,"name":"1-3"}`, []string{"hyprctl -j monitors"}},
		{"special", `{"id":-99,"name":"special:term"}`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, terminal := kittyShell()
			m.answers["hyprctl -j clients"] = answer{out: `[{"address":"0xk","pid":300,"focusHistoryID":1,"workspace":` + c.workspace + `}]`}
			m.answers["hyprctl -j monitors"] = answer{out: `[{"activeWorkspace":{"id":1,"name":"2-1"}},{"activeWorkspace":{"id":13,"name":"1-3"}}]`}
			m.focuser("linux", hyprEnv).Focus(context.Background(), "codex", "s1", terminal)
			want := append(append([]string{"hyprctl -j clients"}, c.want...), "hyprctl dispatch focuswindow address:0xk")
			if got := m.calls[len(m.calls)-len(want):]; !reflect.DeepEqual(got, want) {
				t.Fatalf("calls:\n%s", strings.Join(m.calls, "\n"))
			}
		})
	}
}

// A Hyprland configured in Lua evaluates what `hyprctl dispatch` is given as
// Lua and says so; the dispatchers are then given again as Lua.
func TestHyprlandConfiguredInLua(t *testing.T) {
	m, terminal := kittyShell()
	refused := answer{
		out: "error: [string \"return hl.dispatch(focuswindow address:0xk)\"]:1: ')' expected near 'address'\n\n → Note: dispatch in lua is a shorthand for hl.dispatch(...), your syntax might need to be updated.",
		err: errors.New("exit status 7"),
	}
	delete(m.answers, "hyprctl dispatch")
	m.answers["hyprctl dispatch focuswindow"] = refused
	m.answers["hyprctl dispatch workspace"] = refused
	m.answers["hyprctl dispatch hl."] = answer{out: "ok\n"}
	m.answers["hyprctl -j clients"] = answer{out: `[{"address":"0xk","pid":300,"focusHistoryID":1,"workspace":{"id":-98,"name":"notes"}}]`}
	m.answers["hyprctl -j monitors"] = answer{out: `[{"activeWorkspace":{"id":1,"name":"1"}}]`}
	result := m.focuser("linux", hyprEnv).Focus(context.Background(), "codex", "s1", terminal)
	want := []string{
		"hyprctl -j clients",
		"hyprctl -j monitors",
		"hyprctl dispatch workspace name:notes",
		`hyprctl dispatch hl.dsp.focus({ workspace = "name:notes" })`,
		"hyprctl dispatch focuswindow address:0xk",
		`hyprctl dispatch hl.dsp.focus({ window = "address:0xk" })`,
	}
	if result.Precision != Window || !reflect.DeepEqual(m.calls[len(m.calls)-len(want):], want) {
		t.Fatalf("result = %+v, calls:\n%s", result, strings.Join(m.calls, "\n"))
	}
}

// On macOS kitten is found beside kitty in its bundle when it is not on the
// daemon's PATH.
func TestKittenFromKittysBundle(t *testing.T) {
	m, terminal := kittyShell()
	m.exes = map[int]string{300: "/Applications/kitty.app/Contents/MacOS/kitty"}
	terminal.Env["KITTY_LISTEN_ON"] = "unix:/tmp/kitty-300"
	result := m.focuser("darwin", nil).Focus(context.Background(), "codex", "s1", terminal)
	want := []string{"/Applications/kitty.app/Contents/MacOS/kitten @ --to unix:/tmp/kitty-300 focus-window --match pid:101", "open -a /Applications/kitty.app"}
	if result != (Result{Precision: Session}) || !reflect.DeepEqual(m.calls, want) {
		t.Fatalf("result = %+v, calls:\n%s", result, strings.Join(m.calls, "\n"))
	}
}

func TestKittyWithoutRemoteControlRaisesItsWindow(t *testing.T) {
	m, terminal := kittyShell()
	result := m.focuser("linux", hyprEnv).Focus(context.Background(), "codex", "s1", terminal)
	if result.Precision != Window || !strings.Contains(result.Message, "allow_remote_control") {
		t.Fatalf("result = %+v", result)
	}
}

func TestX11(t *testing.T) {
	m, terminal := kittyShell()
	m.paths = map[string]bool{"xdotool": true}
	m.answers = map[string]answer{
		"xdotool search --onlyvisible --pid 100": {err: errors.New("exit status 1")},
		"xdotool search --onlyvisible --pid 101": {err: errors.New("exit status 1")},
		"xdotool search --onlyvisible --pid 300": {out: "41943041\n41943050\n"},
	}
	result := m.focuser("linux", map[string]string{"DISPLAY": ":0"}).Focus(context.Background(), "codex", "s1", terminal)
	if result.Precision != Window || m.calls[len(m.calls)-1] != "xdotool windowactivate 41943050" {
		t.Fatalf("result = %+v, calls:\n%s", result, strings.Join(m.calls, "\n"))
	}
}

// A minimized window is unmapped, which --onlyvisible leaves out.
func TestX11MinimizedWindow(t *testing.T) {
	m, terminal := kittyShell()
	m.paths = map[string]bool{"xdotool": true}
	m.answers = map[string]answer{
		"xdotool search --onlyvisible": {err: errors.New("exit status 1")},
		"xdotool search --pid 100":     {err: errors.New("exit status 1")},
		"xdotool search --pid 101":     {err: errors.New("exit status 1")},
		"xdotool search --pid 300":     {out: "41943041\n41943050\n"},
	}
	result := m.focuser("linux", map[string]string{"DISPLAY": ":0"}).Focus(context.Background(), "codex", "s1", terminal)
	if result.Precision != Window || m.calls[len(m.calls)-1] != "xdotool windowactivate 41943050" {
		t.Fatalf("result = %+v, calls:\n%s", result, strings.Join(m.calls, "\n"))
	}
}

func TestX11WithWmctrl(t *testing.T) {
	m, terminal := kittyShell()
	m.paths = map[string]bool{"wmctrl": true}
	m.answers = map[string]answer{"wmctrl -lp": {out: "0x01 0 999 host other\n0x02 0 300 host kitty\n"}}
	result := m.focuser("linux", map[string]string{"DISPLAY": ":0"}).Focus(context.Background(), "codex", "s1", terminal)
	if result.Precision != Window || m.calls[len(m.calls)-1] != "wmctrl -i -a 0x02" {
		t.Fatalf("result = %+v, calls:\n%s", result, strings.Join(m.calls, "\n"))
	}
}

func TestOtherWaylandCompositor(t *testing.T) {
	m, terminal := kittyShell()
	f := m.focuser("linux", map[string]string{"WAYLAND_DISPLAY": "wayland-0", "DISPLAY": ":0"})
	if got := f.Predict("codex", terminal); got != "" {
		t.Fatalf("Predict = %q", got)
	}
	result := f.Focus(context.Background(), "codex", "s1", terminal)
	if result.Precision != "" || !strings.Contains(result.Message, "Hyprland and X11") {
		t.Fatalf("result = %+v", result)
	}
}

// macTerminal is a session in Terminal 8's tab on /dev/ttys004.
func macTerminal(app string) (*machine, *hookevents.Terminal) {
	m := &machine{
		procs: map[int]proc{10: {"claude", "a", 11}, 11: {"zsh", "b", 12}, 12: {"login", "c", 8}, 8: {app, "d", 1}},
		exes:  map[int]string{8: "/Applications/Utilities/" + app + ".app/Contents/MacOS/" + app},
	}
	return m, &hookevents.Terminal{TTY: "/dev/ttys004", Processes: m.recorded(10)}
}

func TestAppleTerminalTab(t *testing.T) {
	m, terminal := macTerminal("Terminal")
	m.answers = map[string]answer{"osascript": {out: "found\n"}}
	f := m.focuser("darwin", nil)
	if got := f.Predict("claude-code", terminal); got != Session {
		t.Fatalf("Predict = %q", got)
	}
	result := f.Focus(context.Background(), "claude-code", "s1", terminal)
	if result != (Result{Precision: Session}) || len(m.calls) != 1 {
		t.Fatalf("result = %+v, calls %v", result, m.calls)
	}
	if !strings.Contains(m.calls[0], `tell application "Terminal"`) || !strings.HasSuffix(m.calls[0], " /dev/ttys004") {
		t.Fatalf("call = %s", m.calls[0])
	}
}

func TestITermWithoutAutomationRaisesTheApp(t *testing.T) {
	m, terminal := macTerminal("iTermServer-3.5")
	m.answers = map[string]answer{"osascript": {err: errors.New("execution error: Not authorized to send Apple events to iTerm2. (-1743)")}}
	result := m.focuser("darwin", nil).Focus(context.Background(), "claude-code", "s1", terminal)
	if result.Precision != Window || !strings.Contains(result.Message, "Automation") {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(m.calls[0], `tell application "iTerm2"`) || m.calls[1] != "open -b com.googlecode.iterm2" {
		t.Fatalf("calls:\n%s", strings.Join(m.calls, "\n"))
	}
}

func TestClaudeDesktopConversation(t *testing.T) {
	dir := t.TempDir()
	sessions := filepath.Join(dir, "claude-code-sessions", "account", "org")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(sessions, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("local_old.json", `{"sessionId":"local_old","cliSessionId":"cli-1","isArchived":true}`)
	write("local_abc-123.json", `{"sessionId":"local_abc-123","cliSessionId":"cli-2","previousCliSessionIds":["cli-1"]}`)
	m := &machine{
		procs: map[int]proc{100: {"claude", "a", 90}, 90: {"claude-desktop", "b", 1}},
		answers: map[string]answer{
			"hyprctl -j clients": {out: `[{"address":"0xc","pid":90,"focusHistoryID":3}]`},
			"hyprctl dispatch":   {out: "ok"},
		},
	}
	f := m.focuser("linux", hyprEnv)
	f.ClaudeDir = dir
	terminal := &hookevents.Terminal{Processes: m.recorded(100)}
	if got := f.Predict("claude-code", terminal); got != Session {
		t.Fatalf("Predict = %q", got)
	}
	result := f.Focus(context.Background(), "claude-code", "cli-1", terminal)
	want := []string{"xdg-open claude://code/continue?session=local_abc-123", "hyprctl -j clients", "hyprctl dispatch focuswindow address:0xc"}
	if result != (Result{Precision: Session}) || !reflect.DeepEqual(m.calls, want) {
		t.Fatalf("result = %+v, calls:\n%s", result, strings.Join(m.calls, "\n"))
	}
	// A session Claude desktop does not have is raised as any other.
	m.calls = nil
	if result := f.Focus(context.Background(), "claude-code", "cli-3", terminal); result.Precision != Window || m.calls[0] != "hyprctl -j clients" {
		t.Fatalf("result = %+v, calls %v", result, m.calls)
	}
	// A session continued in Claude desktop that still runs in a terminal is
	// brought forward there.
	m.procs[90] = proc{"kitty", "b", 1}
	m.calls = nil
	if result := f.Focus(context.Background(), "claude-code", "cli-1", &hookevents.Terminal{Processes: m.recorded(100)}); result.Precision != Window ||
		slices.ContainsFunc(m.calls, func(c string) bool { return strings.HasPrefix(c, "xdg-open") }) {
		t.Fatalf("result = %+v, calls %v", result, m.calls)
	}
}

func TestPredict(t *testing.T) {
	m := tmuxInKitty()
	f := m.focuser("linux", hyprEnv)
	if got := f.Predict("codex", tmuxTerminal(m)); got != Session {
		t.Fatalf("tmux: %q", got)
	}
	_, kitty := kittyShell()
	if got := f.Predict("codex", kitty); got != Window {
		t.Fatalf("kitty without remote control: %q", got)
	}
	kitty.Env["KITTY_LISTEN_ON"] = "unix:/tmp/kitty-300"
	if got := f.Predict("codex", kitty); got != Session {
		t.Fatalf("kitty: %q", got)
	}
	if got := f.Predict("codex", nil); got != "" {
		t.Fatalf("nothing recorded: %q", got)
	}
	if got := m.focuser("windows", nil).Predict("codex", kitty); got != "" {
		t.Fatalf("windows: %q", got)
	}
}

func TestAppBundle(t *testing.T) {
	for exe, want := range map[string]string{
		"/Applications/Claude.app/Contents/Frameworks/Claude Helper.app/Contents/MacOS/Claude Helper": "/Applications/Claude.app",
		"/Applications/iTerm.app/Contents/MacOS/iTermServer-3.5":                                      "/Applications/iTerm.app",
		"/usr/bin/login": "",
	} {
		if got := appBundle(exe); got != want {
			t.Errorf("appBundle(%q) = %q, want %q", exe, got, want)
		}
	}
}
