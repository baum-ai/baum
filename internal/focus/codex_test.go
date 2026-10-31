package focus

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/hookevents"
)

const codexThread = "019f0000-1111-7222-8333-444444444444"

func sharedCodexMachine() (*machine, *hookevents.Terminal) {
	m := tmuxInKitty()
	m.procs[100] = proc{"codex", "a", 101}
	m.procs[101] = proc{"codex", "b", 50}
	m.procs[50] = proc{"systemd", "c", 1}
	terminal := tmuxTerminal(m)
	terminal.TTY = ""
	terminal.Env["TMUX_PANE"] = "%2"
	return m, terminal
}

func TestSharedCodexFindsItsTmuxPane(t *testing.T) {
	for _, title := range []string{codexThread, "Working | repo | " + codexThread, "Working | repo | " + codexThread[:29] + "..."} {
		t.Run(title, func(t *testing.T) {
			m, terminal := sharedCodexMachine()
			m.answers["tmux -S /tmp/tmux-1000/default list-panes"] = answer{out: "%2\t/dev/pts/2\tcodex\tOther thread\n%7\t/dev/pts/12\tcodex\t" + title + "\n"}
			f := m.focuser("linux", hyprEnv)
			if f.Predict("codex", terminal) != Session {
				t.Fatal("missing focus action")
			}
			result := f.Focus(context.Background(), "codex", codexThread, terminal)
			if result != (Result{Precision: Session}) || !slices.Contains(m.calls, "tmux -S /tmp/tmux-1000/default select-pane -t %7") {
				t.Fatalf("result %+v, calls %v", result, m.calls)
			}
			if terminal.TTY != "" || terminal.Env["TMUX_PANE"] != "%2" {
				t.Fatal("mutated hook record")
			}
		})
	}
}

func TestSharedCodexRejectsAmbiguousTmuxPanes(t *testing.T) {
	m, terminal := sharedCodexMachine()
	m.answers["tmux -S /tmp/tmux-1000/default list-panes"] = answer{out: "%7\t/dev/pts/12\tcodex\t" + codexThread + "\n%8\t/dev/pts/13\tcodex\t" + codexThread + "\n"}
	result := m.focuser("linux", hyprEnv).Focus(context.Background(), "codex", codexThread, terminal)
	if result.Precision != "" || !strings.Contains(result.Message, "More than one") || len(m.calls) != 1 {
		t.Fatalf("result %+v, calls %v", result, m.calls)
	}
}

func TestCodexTitleMatches(t *testing.T) {
	for title, want := range map[string]bool{
		codexThread: true,
		"Busy | repo | " + codexThread[:24] + "…":                    true,
		codexThread[:23] + "...":                                     false,
		codexThread[:29]:                                             false,
		"Working | repo | 019f0000-aaaa-7bbb-8ccc-ddddd...":          false,
		"Name mentions " + codexThread + " but is not the thread ID": false,
		"": false,
	} {
		if got := codexTitleMatches(title, codexThread); got != want {
			t.Errorf("title %q: got %v, want %v", title, got, want)
		}
	}
}

func TestSharedCodexHyprlandWithoutTmux(t *testing.T) {
	for _, staleTmux := range []bool{false, true} {
		m, terminal := sharedCodexMachine()
		if !staleTmux {
			delete(terminal.Env, "TMUX")
		}
		windows := []hyprClient{{Address: "0xthread", PID: 999, Title: "Working | repo | " + codexThread, Workspace: hyprWorkspace{ID: 4, Name: "4"}}}
		out, _ := json.Marshal(windows)
		m.answers["hyprctl -j clients"] = answer{out: string(out)}
		m.answers["hyprctl -j monitors"] = answer{out: `[]`}
		f := m.focuser("linux", hyprEnv)
		if f.Predict("codex", terminal) != Session {
			t.Fatal("missing focus action")
		}
		result := f.Focus(context.Background(), "codex", codexThread, terminal)
		if result != (Result{Precision: Session}) || !slices.Contains(m.calls, "hyprctl dispatch workspace 4") || !slices.Contains(m.calls, "hyprctl dispatch focuswindow address:0xthread") {
			t.Fatalf("stale tmux %v: result %+v, calls %v", staleTmux, result, m.calls)
		}
	}
}

func TestSharedCodexKittyWithoutTmux(t *testing.T) {
	m, terminal := sharedCodexMachine()
	delete(terminal.Env, "TMUX")
	terminal.Env["KITTY_PID"], terminal.Env["KITTY_LISTEN_ON"] = "300", "unix:/tmp/kitty-300"
	m.answers["kitten @ --to unix:/tmp/kitty-300 ls"] = answer{out: `[{"tabs":[{"windows":[{"id":42,"title":"Working | repo | ` + codexThread + `"}]}]}]`}
	result := m.focuser("linux", hyprEnv).Focus(context.Background(), "codex", codexThread, terminal)
	if result != (Result{Precision: Session}) || !slices.Contains(m.calls, "kitten @ --to unix:/tmp/kitty-300 focus-window --match id:42") {
		t.Fatalf("result %+v, calls %v", result, m.calls)
	}
}

func TestSharedCodexX11WithoutTmux(t *testing.T) {
	for _, tool := range []string{"xdotool", "wmctrl"} {
		t.Run(tool, func(t *testing.T) {
			m, terminal := sharedCodexMachine()
			terminal.Env = nil
			m.paths = map[string]bool{tool: true}
			m.answers["xdotool search"] = answer{out: "123\n"}
			m.answers["xdotool getwindowname 123"] = answer{out: "Working | repo | " + codexThread + "\n"}
			m.answers["wmctrl -lp"] = answer{out: "123 0 999 host Working | repo | " + codexThread + "\n"}
			result := m.focuser("linux", map[string]string{"DISPLAY": ":0"}).Focus(context.Background(), "codex", codexThread, terminal)
			want := "xdotool windowactivate 123"
			if tool == "wmctrl" {
				want = "wmctrl -i -a 123"
			}
			if result != (Result{Precision: Session}) || !slices.Contains(m.calls, want) {
				t.Fatalf("result %+v, calls %v", result, m.calls)
			}
		})
	}
}

func TestSharedCodexAppleTabsWithoutTmux(t *testing.T) {
	for _, app := range []string{"Terminal", "iTerm2"} {
		t.Run(app, func(t *testing.T) {
			m, terminal := sharedCodexMachine()
			terminal.Env = nil
			m.procs[50] = proc{"launchd", "c", 1}
			f := m.focuser("darwin", nil)
			f.Run = func(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
				call := strings.Join(args, " ")
				m.calls = append(m.calls, name+" "+call)
				if strings.HasSuffix(call, " /dev/ttys004") {
					return []byte("found"), nil
				}
				if strings.Contains(call, `tell application "`+app+`"`) {
					return []byte("/dev/ttys004\tWorking | repo | " + codexThread + "\n"), nil
				}
				return nil, nil
			}
			result := f.Focus(context.Background(), "codex", codexThread, terminal)
			if result != (Result{Precision: Session}) || len(m.calls) != 3 || !strings.HasSuffix(m.calls[2], " /dev/ttys004") {
				t.Fatalf("result %+v, calls %v", result, m.calls)
			}
		})
	}
}

func TestSharedCodexMissingOrAmbiguousNativeWindows(t *testing.T) {
	for _, count := range []int{0, 2} {
		m, terminal := sharedCodexMachine()
		terminal.Env = nil
		windows := make([]hyprClient, count)
		for i := range windows {
			windows[i] = hyprClient{Title: codexThread, Address: "0xthread"}
		}
		out, _ := json.Marshal(windows)
		m.answers["hyprctl -j clients"] = answer{out: string(out)}
		result := m.focuser("linux", hyprEnv).Focus(context.Background(), "codex", codexThread, terminal)
		if result.Precision != "" || len(m.calls) != 1 {
			t.Fatalf("count %d: result %+v, calls %v", count, result, m.calls)
		}
	}
}

func TestSharedCodexAppleAutomationRefused(t *testing.T) {
	m, terminal := sharedCodexMachine()
	terminal.Env = nil
	m.answers["osascript"] = answer{err: errors.New("Not authorized (-1743)")}
	result := m.focuser("darwin", nil).Focus(context.Background(), "codex", codexThread, terminal)
	if result.Precision != "" || !strings.Contains(result.Message, "Automation") {
		t.Fatalf("result %+v", result)
	}
}
