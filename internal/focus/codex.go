package focus

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/baum-ai/baum/internal/hookevents"
)

func codexTitleMatches(title, id string) bool {
	if id == "" {
		return false
	}
	if i := strings.LastIndex(title, " | "); i >= 0 {
		title = title[i+3:]
	}
	title = strings.TrimSpace(title)
	prefix := strings.TrimSuffix(strings.TrimSuffix(title, "..."), "…")
	return title == id || prefix != title && len(prefix) >= 24 && strings.HasPrefix(id, prefix)
}

// The TUI, rather than its shared app server, owns the terminal title. The
// thread-id title item lets the terminal APIs locate it without resuming a
// second copy of the conversation or trusting the daemon's inherited pane.
func (f *Focuser) codexNative(ctx context.Context, id string, t *hookevents.Terminal) Result {
	if result, found := f.codexKitty(ctx, id, t); found {
		return result
	}
	if f.goos() == "darwin" {
		return f.codexApple(ctx, id)
	}
	env := f.environment(t.Env)
	switch {
	case hyprland(env):
		return f.codexHyprland(ctx, id, env)
	case x11(env):
		return f.codexX11(ctx, id, env)
	}
	return Result{Message: "Bringing windows forward works on Hyprland and X11; this desktop is neither."}
}

const codexTitleHint = "Could not find this Codex session's terminal. Open the session in Codex and include thread-id in its tui.terminal_title setting."
const codexAmbiguous = "More than one terminal matches this Codex session. Leave it open in one terminal, then try again."
const codexTmuxAmbiguous = "More than one Codex terminal matches this session. Leave it open in one pane, then try again."

func (f *Focuser) codexKitty(ctx context.Context, id string, t *hookevents.Terminal) (Result, bool) {
	pid, _ := strconv.Atoi(t.Env["KITTY_PID"])
	p, _, alive := f.process(pid)
	if !alive || p.Name != "kitty" || t.Env["KITTY_LISTEN_ON"] == "" {
		return Result{}, false
	}
	kitten := "kitten"
	if _, err := f.lookPath(kitten); err != nil {
		if exe := f.executable(pid); exe != "" {
			kitten = filepath.Join(filepath.Dir(exe), "kitten")
		}
	}
	out, err := f.run(ctx, nil, kitten, "@", "--to", t.Env["KITTY_LISTEN_ON"], "ls")
	if err != nil {
		return Result{}, false
	}
	var windows []struct {
		Tabs []struct {
			Windows []struct {
				ID    int    `json:"id"`
				Title string `json:"title"`
			} `json:"windows"`
		} `json:"tabs"`
	}
	if json.Unmarshal(out, &windows) != nil {
		return Result{}, false
	}
	wanted := ""
	for _, w := range windows {
		for _, tab := range w.Tabs {
			for _, window := range tab.Windows {
				if !codexTitleMatches(window.Title, id) {
					continue
				}
				if wanted != "" {
					return Result{Message: codexAmbiguous}, true
				}
				wanted = strconv.Itoa(window.ID)
			}
		}
	}
	if wanted == "" {
		return Result{}, false
	}
	env := maps.Clone(t.Env)
	env["KITTY_WINDOW_ID"] = wanted
	chain := []hookevents.Process{p}
	tab := f.kittyTab(ctx, chain, 0, env, false)
	if !tab.selected {
		return Result{Message: tab.hint}, true
	}
	raised, why := f.raise(ctx, chain, f.environment(t.Env))
	if !raised {
		return Result{Precision: Session, Message: "Selected the session, but could not bring its window forward. " + why}, true
	}
	return Result{Precision: Session}, true
}

func (f *Focuser) codexHyprland(ctx context.Context, id string, env map[string]string) Result {
	vars := pairs(env)
	out, err := f.run(ctx, vars, "hyprctl", "-j", "clients")
	if err != nil {
		return Result{Message: fmt.Sprintf("Could not list Hyprland's windows: %v.", err)}
	}
	var windows []hyprClient
	if json.Unmarshal(out, &windows) != nil {
		return Result{Message: "Could not read Hyprland's windows."}
	}
	var wanted *hyprClient
	for i, w := range windows {
		if !codexTitleMatches(w.Title, id) || w.Address == "" {
			continue
		}
		if wanted != nil {
			return Result{Message: codexAmbiguous}
		}
		wanted = &windows[i]
	}
	if wanted == nil {
		return Result{Message: codexTitleHint}
	}
	f.hyprShowWorkspace(ctx, vars, wanted.Workspace)
	target := "address:" + wanted.Address
	outcome, err := f.hyprDispatch(ctx, vars, "hl.dsp.focus({ window = "+strconv.Quote(target)+" })", "focuswindow", target)
	if err != nil || outcome != "ok" {
		return Result{Message: fmt.Sprintf("Hyprland did not focus the window: %s.", cmpOr(outcome, fmt.Sprint(err)))}
	}
	return Result{Precision: Session}
}

func (f *Focuser) codexX11(ctx context.Context, id string, env map[string]string) Result {
	vars := pairs(env)
	wanted := ""
	remember := func(window, title string) bool {
		if !codexTitleMatches(title, id) {
			return true
		}
		if wanted != "" {
			return false
		}
		wanted = window
		return true
	}
	command, args := "wmctrl", []string{"-i", "-a"}
	if _, err := f.lookPath("xdotool"); err == nil {
		query := id
		if len(query) > 24 {
			query = query[:24]
		}
		out, _ := f.run(ctx, vars, "xdotool", "search", "--name", query)
		for _, window := range strings.Fields(string(out)) {
			title, err := f.run(ctx, vars, "xdotool", "getwindowname", window)
			if err == nil && !remember(window, string(title)) {
				return Result{Message: codexAmbiguous}
			}
		}
		command, args = "xdotool", []string{"windowactivate"}
	} else if _, err := f.lookPath("wmctrl"); err == nil {
		out, err := f.run(ctx, vars, "wmctrl", "-lp")
		if err != nil {
			return Result{Message: fmt.Sprintf("Could not list the windows: %v.", err)}
		}
		for line := range strings.SplitSeq(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 5 && !remember(fields[0], strings.Join(fields[4:], " ")) {
				return Result{Message: codexAmbiguous}
			}
		}
	} else {
		return Result{Message: "Install xdotool or wmctrl to bring windows forward on X11."}
	}
	if wanted == "" {
		return Result{Message: codexTitleHint}
	}
	if _, err := f.run(ctx, vars, command, append(args, wanted)...); err != nil {
		return Result{Message: fmt.Sprintf("Could not bring the window forward: %v.", err)}
	}
	return Result{Precision: Session}
}

func (f *Focuser) codexApple(ctx context.Context, id string) Result {
	type match struct{ app, tty string }
	var matches []match
	var hint string
	for _, app := range []string{"iTerm2", "Terminal"} {
		var body []string
		if app == "iTerm2" {
			body = []string{`repeat with w in windows`, `repeat with t in tabs of w`, `repeat with s in sessions of t`, `set rows to rows & (tty of s) & tab & (name of s) & linefeed`, `end repeat`, `end repeat`, `end repeat`}
		} else {
			body = []string{`repeat with w in windows`, `repeat with t in tabs of w`, `set rows to rows & (tty of t) & tab & (name of t) & linefeed`, `end repeat`, `end repeat`}
		}
		script := append([]string{`if application "` + app + `" is not running then return ""`, `set rows to ""`, `tell application "` + app + `"`}, body...)
		script = append(script, `end tell`, `return rows`)
		var args []string
		for _, line := range script {
			args = append(args, "-e", line)
		}
		out, err := f.run(ctx, nil, "osascript", args...)
		if err != nil {
			hint = fmt.Sprintf("Could not read %s's tabs: %v.", app, err)
			if strings.Contains(err.Error(), "-1743") {
				hint = fmt.Sprintf("To select %s's tab, allow baum to control %s in System Settings, Privacy & Security, Automation.", app, app)
			}
			continue
		}
		for row := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
			tty, title, ok := strings.Cut(row, "\t")
			if ok && tty != "" && codexTitleMatches(title, id) {
				matches = append(matches, match{app, tty})
			}
		}
	}
	if len(matches) > 1 {
		return Result{Message: codexAmbiguous}
	}
	if len(matches) == 0 {
		return Result{Message: cmpOr(hint, codexTitleHint)}
	}
	wanted := matches[0]
	script := terminalScript
	if wanted.app == "iTerm2" {
		script = iTermScript
	}
	tab := f.appleScriptTab(ctx, wanted.app, script, wanted.tty)
	if tab.activated && tab.selected {
		return Result{Precision: Session}
	}
	return Result{Message: cmpOr(tab.hint, codexTitleHint)}
}
