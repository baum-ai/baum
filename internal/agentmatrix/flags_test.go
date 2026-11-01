package agentmatrix

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/claude"
	"github.com/baum-ai/baum/agent/codex"
)

// checkVersion runs the agent's --version and baum's doctor, which applies
// the version floor, and compares both with the release the matrix asked for.
func (m *matrix) checkVersion() {
	h := m.newHome()
	out, _, err := h.run(h.dir, 30*time.Second, "", m.bin, "--version")
	if err != nil {
		m.record("version", "fail", "%s --version: %v: %s", m.bin, err, oneLine(out))
		return
	}
	got := versionPattern.FindString(out)
	if got != m.version {
		m.record("version", "fail", "installed %q, want %s", oneLine(out), m.version)
		return
	}
	m.record("version", "pass", "--version reports %q", oneLine(out))

	// Doctor exits 6 when it finds a problem, which is what a refused release is.
	_, out, _, _ = h.run2(h.dir, 3*time.Minute, "", m.baum, "internal", "doctor", "--json", "--offline")
	var report struct {
		Checks []struct{ ID, Status, Detail, Version, Fix string }
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		m.record("doctor", "fail", "doctor printed no JSON: %s", oneLine(out))
		return
	}
	id := map[string]string{"claude-code": "claude", "codex": "codex"}[m.agent]
	for _, c := range report.Checks {
		if c.ID != id {
			continue
		}
		detail := fmt.Sprintf("doctor %s: version %q: %s %s", c.Status, c.Version, c.Detail, c.Fix)
		refused := strings.Contains(c.Detail, "older than")
		switch {
		case strings.Contains(c.Detail, "could not start its read-only sandbox"):
			m.record("doctor", "skip", "the sandbox part of doctor cannot run in this container: %s", oneLine(detail))
		case c.Status == "ok" && m.support == "supported" && c.Version == m.version:
			m.record("doctor", "pass", "%s", oneLine(detail))
		case refused && m.support == "below":
			m.record("doctor", "pass", "refused as expected: %s", oneLine(detail))
		default:
			m.record("doctor", "fail", "%s", oneLine(detail))
		}
		return
	}
	m.record("doctor", "fail", "doctor has no %s check: %s", id, oneLine(out))
}

// recorder is a stand-in for the agent that logs the arguments baum passes it
// and fails. --version goes to the real executable, as baum's own checks need.
// Codex's sandbox check also runs through it, and Docker's default profile
// blocks the user namespaces the real sandbox needs, so the stand-in answers
// the two probes baum makes (a read that must work and a write that must fail)
// as an enforced read-only sandbox would, only so that baum goes on to build
// the arguments being recorded. The sandbox check tests the real one.
func (m *matrix) recorder(dir string) (script, argvDir string) {
	argvDir = filepath.Join(dir, "argv")
	if err := os.MkdirAll(argvDir, 0o700); err != nil {
		m.t.Fatal(err)
	}
	script = filepath.Join(dir, "recorder", filepath.Base(m.bin))
	if err := os.MkdirAll(filepath.Dir(script), 0o700); err != nil {
		m.t.Fatal(err)
	}
	body := fmt.Sprintf(`#!/bin/sh
case "${1:-}" in
--version) exec %q "$@" ;;
sandbox)
  while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do shift; done
  shift
  if [ "${1:-}" = cat ]; then exec "$@"; fi
  echo baum-write-blocked
  exit 0 ;;
esac
printf '%%s\0' "$@" > %q/$(date +%%s%%N)-$$.argv
exit 3
`, m.bin, argvDir)
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		m.t.Fatal(err)
	}
	return script, argvDir
}

// checkSandbox runs Codex's read-only sandbox as baum's preflight does. A
// container that forbids user namespaces cannot start it, which says nothing
// about the release.
func (m *matrix) checkSandbox() {
	if m.agent != "codex" {
		m.record("sandbox", "skip", "Claude Code has no sandbox check")
		return
	}
	h := m.newHome()
	file := filepath.Join(h.dir, "file")
	if err := os.WriteFile(file, []byte("baum sandbox check"), 0o600); err != nil {
		m.t.Fatal(err)
	}
	out, _, err := h.run(h.dir, time.Minute, "", m.bin, "sandbox", "-P", ":read-only", "--", "cat", file)
	switch {
	case err == nil && strings.Contains(out, "baum sandbox check"):
		m.record("sandbox", "pass", "codex sandbox -P :read-only runs")
	case strings.Contains(out, "namespace"):
		m.record("sandbox", "skip", "this container forbids user namespaces: %s", oneLine(out))
	case m.support == "below" && refusal(out) != "":
		m.record("sandbox", "pass", "release below baum's floor lacks it: %s", oneLine(out))
	default:
		m.record("sandbox", "fail", "%v: %s", err, oneLine(out))
	}
}

const reviewSchema = `{"type":"object","properties":{"summary":{"type":"string"}},"required":["summary"],"additionalProperties":false}`

const sessionUUID = "11111111-2222-3333-4444-555555555555"

// parserRefusals are the wording of an agent's own argument parser refusing
// what baum passed; any other failure (no login, no network) means the
// arguments were accepted.
var parserRefusals = []string{"unknown option", "unknown argument", "unexpected argument", "unknown command", "unrecognized subcommand",
	"unrecognized option", "invalid value", "is invalid", "too many arguments", "required arguments", "error: option", "did you mean"}

func refusal(output string) string {
	for _, line := range strings.Split(output, "\n") {
		l := strings.ToLower(line)
		for _, p := range parserRefusals {
			if strings.Contains(l, p) {
				return strings.TrimSpace(line)
			}
		}
	}
	return ""
}

// checkFlags has baum's Claude Code and Codex backends build the arguments of
// every kind of job (review, review keeping its session, resume, fork, fix and
// the one-shot completion) for this release, then runs that exact argument
// list on the real executable, with no login and no network, and reports
// whether its argument parser accepts it. A release below the floor must be
// refused with an upgrade message instead.
func (m *matrix) checkFlags() {
	h := m.newHome()
	h.setenv(m.t)
	work := m.t.TempDir()
	script, argvDir := m.recorder(work)
	scratch := filepath.Join(work, "scratch")
	for _, d := range []string{"cwd", "scratch"} {
		if err := os.MkdirAll(filepath.Join(work, d), 0o700); err != nil {
			m.t.Fatal(err)
		}
	}
	cwd := filepath.Join(work, "cwd")
	base := agent.Request{Directory: cwd, Prompt: "hello", Schema: []byte(reviewSchema), MaxCalls: 4, MaxTokens: 100000}
	session := base
	session.PersistSession = true
	resume := base
	resume.ResumeSession = sessionUUID
	fork := resume
	fork.ForkSession = true
	fix := base
	fix.Schema, fix.Scratch = nil, scratch

	var backend interface {
		agent.Backend
		agent.Fixer
		agent.Completer
	}
	switch m.agent {
	case "claude-code":
		backend = &claude.Backend{Binary: script, Model: "sonnet", Effort: "high", Profile: claude.User}
	default:
		backend = &codex.Backend{Binary: script, Model: "gpt-5", Effort: "high", Profile: codex.Local}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	type job struct {
		name string
		run  func() error
	}
	jobs := []job{
		{"review", func() error { _, err := backend.Run(ctx, base); return err }},
		{"review-keep-session", func() error { _, err := backend.Run(ctx, session); return err }},
		{"resume", func() error { _, err := backend.Run(ctx, resume); return err }},
		{"fork", func() error { _, err := backend.Run(ctx, fork); return err }},
		{"fix", func() error { _, err := backend.Fix(ctx, fix); return err }},
		{"complete", func() error {
			_, err := backend.Complete(ctx, agent.CompleteRequest{System: "sys", User: "usr", Schema: []byte(reviewSchema)})
			return err
		}},
	}
	var problems, accepted []string
	for _, j := range jobs {
		before := argvFiles(argvDir)
		err := j.run()
		fresh := argvFiles(argvDir)[len(before):]
		if len(fresh) == 0 {
			// baum never started the agent: its own checks refused it.
			msg := "baum did not start the agent"
			if err != nil {
				msg = err.Error()
			}
			if m.support == "below" && strings.Contains(msg, "older than") {
				m.record("flags", "pass", "refused as expected: %s", oneLine(msg))
				return
			}
			problems = append(problems, j.name+": "+oneLine(msg))
			continue
		}
		if m.support == "below" {
			problems = append(problems, j.name+": baum ran a release below its floor")
			continue
		}
		argv := readArgv(fresh[0])
		out, _, runErr := h.run(cwd, 60*time.Second, "hello", m.bin, argv...)
		switch line := refusal(out); {
		case line != "":
			problems = append(problems, j.name+": agent refused its arguments: "+oneLine(line))
		case runErr != nil && strings.Contains(runErr.Error(), "deadline"):
			problems = append(problems, j.name+": agent did not exit within 60s: "+oneLine(out))
		default:
			accepted = append(accepted, j.name)
		}
	}
	if len(problems) > 0 {
		m.record("flags", "fail", "accepted %v; %s", accepted, strings.Join(problems, "; "))
		return
	}
	m.record("flags", "pass", "argument parser accepts baum's arguments for %s", strings.Join(accepted, ", "))
}

func argvFiles(dir string) []string {
	files, _ := filepath.Glob(filepath.Join(dir, "*.argv"))
	sort.Strings(files)
	return files
}

func readArgv(path string) []string {
	data, _ := os.ReadFile(path)
	args := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
	return args
}
