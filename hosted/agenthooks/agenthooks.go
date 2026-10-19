// Package agenthooks holds the committed agent-hook vocabulary: which events
// each coding agent has, the exact command string a hook entry carries, and how
// to recognise one that is already ours.
//
// It exists because two callers must agree on those strings byte for byte.
// `baum setup` rewrites the hooks in a checkout it is standing in
// (hosted/baumcli), and the hosted service's onboarding pull request writes the
// same files into a repository nobody has checked out. A
// repository set up either way has to end up with identical files, or the two
// paths would fight over the same lines forever.
//
// Customer repositories keep these command strings in tracked files, so they
// are frozen once they land: change what a hook *does* in the binary, not in
// the string that invokes it. The strings run baum only when `baum hooks agent
// --check` succeeds, which it does only for a user who opted in to the hosted
// service; everyone else runs Entire directly, as if baum were not installed.
package agenthooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// EntireVersion is the Entire CLI release baum pins. Everything derived
// from an Entire artifact — the installed binary, the captured onboarding
// templates — is keyed to it, so a bump is a deliberate act with tests to
// match rather than a silent drift.
const EntireVersion = "0.11.2"

const (
	// SessionStartEvent is the one event that does more than delegate: it runs
	// the bootstrap first and reports the install notice when Entire is absent.
	SessionStartEvent = "session-start"

	entireMissingNotice = "Entire CLI is enabled but not installed or not on PATH."
	entireInstallGuide  = "Installation guide: https://docs.entire.io/cli/installation#installation-methods"

	// entireHookGuard opens every command `entire enable` generates: the hook
	// falls back to a notice or a no-op when Entire is not installed.
	entireHookGuard = "if ! command -v entire >/dev/null 2>&1; then "
)

// Spec describes the hook surface baum owns for one coding agent: the
// events Entire accepts, the committed file holding the dispatcher commands,
// and the system message session start reports when Entire is missing. An empty
// MissingNote means that agent's session start stays silent.
type Spec struct {
	Slug         string
	SettingsFile string
	Events       []string
	MissingNote  string
}

var specs = []Spec{
	{
		Slug:         "claude-code",
		SettingsFile: ".claude/settings.json",
		Events:       []string{"session-start", "user-prompt-submit", "pre-task", "post-task", "post-todo", "stop", "session-end", "subagent-stop"},
		MissingNote:  "\n\n" + entireMissingNotice + "\n" + entireInstallGuide,
	},
	{
		Slug:         "codex",
		SettingsFile: ".codex/hooks.json",
		Events: []string{
			"session-start", "user-prompt-submit", "post-tool-use", "stop",
			// Added in Entire 0.10.2, and safe in both directions of a
			// staggered upgrade: a clone whose baum predates them fails
			// `--check` and falls through to Entire, and an Entire that predates
			// them prints its help and exits 0 rather than failing the hook.
			"session-end", "subagent-start", "subagent-stop",
		},
		MissingNote: entireMissingNotice + " " + entireInstallGuide,
	},
	{
		Slug:         "cursor",
		SettingsFile: ".cursor/hooks.json",
		Events:       []string{"session-start", "before-submit-prompt", "pre-compact", "stop", "session-end", "subagent-start", "subagent-stop"},
	},
}

// All returns every agent baum owns hooks for, in a fixed order.
func All() []Spec { return slices.Clone(specs) }

func Lookup(agent string) (Spec, bool) {
	for _, spec := range specs {
		if spec.Slug == agent {
			return spec, true
		}
	}
	return Spec{}, false
}

func Validate(agent, event string) error {
	spec, ok := Lookup(agent)
	if !ok {
		return fmt.Errorf("unsupported coding agent %q", agent)
	}
	if !slices.Contains(spec.Events, event) {
		return fmt.Errorf("unsupported %s hook event %q", agent, event)
	}
	return nil
}

// MissingEntireNotice renders the payload an agent hook prints when Entire is
// not installed, matching what the committed dispatcher prints on its own when
// baum is missing too.
func MissingEntireNotice(spec Spec, event string) string {
	if event != SessionStartEvent || spec.MissingNote == "" {
		return ""
	}
	encoded, err := EncodeJSON(struct {
		SystemMessage string `json:"systemMessage"`
	}{spec.MissingNote})
	if err != nil {
		return ""
	}
	return string(encoded)
}

// Dispatcher renders the committed hook command for one agent event.
// Customer repositories keep these strings forever, so the dispatcher stays a
// thin chain: baum when it understands the event, Entire when it does
// not, and today's notice or no-op when neither is installed. Its tail is
// Entire's own command verbatim, which is what setup replaces.
func Dispatcher(spec Spec, event string) string {
	return baumPrefix(spec, event) + entireBody(spec, event) + "'"
}

// baumPrefix is the part of the dispatcher that tries baum
// first. Nothing can precede it in a command, so a command carrying it is one
// of ours whatever follows.
func baumPrefix(spec Spec, event string) string {
	return fmt.Sprintf(`sh -c 'if command -v baum >/dev/null 2>&1 && baum hooks agent --check %[1]s %[2]s >/dev/null 2>&1; then exec baum hooks agent %[1]s %[2]s; fi; `,
		spec.Slug, event)
}

// LegacyName is the CLI's name before the product was renamed to baum, spelled
// in parts so the published sources do not carry it.
const LegacyName = "recount" + "able"

// legacyPrefix is the dispatcher prefix written before the product was renamed
// to baum. Repositories set up then still commit it, so it is kept only to
// recognise and replace those hooks; nothing generates or runs it.
func legacyPrefix(spec Spec, event string) string {
	return fmt.Sprintf(`sh -c 'if command -v %[3]s >/dev/null 2>&1 && %[3]s hooks agent --check %[1]s %[2]s >/dev/null 2>&1; then exec %[3]s hooks agent %[1]s %[2]s; fi; `,
		spec.Slug, event, LegacyName)
}

// entireBody renders the command `entire enable` writes for one agent event,
// without the `sh -c` wrapper it comes in: the baum dispatcher keeps it
// verbatim as its own tail.
func entireBody(spec Spec, event string) string {
	fallback := "exit 0"
	if notice := MissingEntireNotice(spec, event); notice != "" {
		fallback = `printf "%s\n" "` + shellDoubleQuoted(notice) + `"; exit 0`
	}
	return entireHookGuard + fallback + entireTail(spec, event)
}

// EntireCommand renders the command `entire enable` writes for one agent
// event, wrapper and all. Setup replaces exactly this string, so it is also
// what a recognition test builds to prove the match.
func EntireCommand(spec Spec, event string) string {
	return "sh -c '" + entireBody(spec, event) + "'"
}

func entireTail(spec Spec, event string) string {
	return fmt.Sprintf("; fi; exec entire hooks %s %s", spec.Slug, event)
}

func shellDoubleQuoted(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`").Replace(value)
}

var commandField = regexp.MustCompile(`"command"\s*:\s*"`)

// OwnCommands replaces recognised Entire hook commands in place and drops every
// later hook entry for the same agent event and tool matcher. Editing the
// JSON text rather than re-encoding a decoded document keeps every unrelated
// key, ordering, and formatting choice the agents wrote.
//
// The repeat copies exist because Entire recognises only its own wrapper: once
// a command dispatches through baum, `entire enable` or `entire agent
// add` appends a fresh hook beside it rather than seeing the event as handled.
// Copies with the same matcher would run the event twice, so the first stays
// and the rest go, whether they are Entire's command or a dispatcher.
func OwnCommands(contents []byte) ([]byte, error) {
	var out bytes.Buffer
	dispatched := map[string]bool{}
	offset := 0
	for offset < len(contents) {
		match := commandField.FindIndex(contents[offset:])
		if match == nil {
			break
		}
		start := offset + match[1] - 1
		end, ok := jsonStringEnd(contents, start)
		if !ok {
			return nil, errors.New("unterminated command string")
		}
		var command string
		if err := json.Unmarshal(contents[start:end], &command); err != nil {
			return nil, fmt.Errorf("decode command: %w", err)
		}
		spec, event, owned := OwnedCommand(command)
		if !owned {
			out.Write(contents[offset:end])
			offset = end
			continue
		}
		matcher, err := enclosingMatcher(contents, start)
		if err != nil {
			return nil, err
		}
		key := spec.Slug + "/" + event + "/" + matcher
		if dispatched[key] {
			from, to, ok := hookEntryBounds(contents, start)
			if !ok || from < offset {
				return nil, fmt.Errorf("hook command for %s %s is not inside a JSON object", spec.Slug, event)
			}
			out.Write(contents[offset:from])
			offset = to
			continue
		}
		dispatched[key] = true
		out.Write(contents[offset:start])
		encoded, err := EncodeJSON(Dispatcher(spec, event))
		if err != nil {
			return nil, err
		}
		out.Write(encoded)
		offset = end
	}
	out.Write(contents[offset:])
	return out.Bytes(), nil
}

// enclosingMatcher distinguishes hooks for different tools that dispatch the
// same Entire event. Claude Code uses both Agent and Task, for example, and
// removing either command would stop capturing sessions on that tool.
func enclosingMatcher(contents []byte, commandStart int) (string, error) {
	entryStart, _, ok := enclosingObject(contents, commandStart)
	if !ok || entryStart == 0 {
		return "", errors.New("hook command is not inside a JSON object")
	}
	parentStart, parentEnd, ok := enclosingObject(contents, entryStart-1)
	if !ok {
		return "", errors.New("hook command has no parent JSON object")
	}
	var parent struct {
		Matcher *string `json:"matcher"`
	}
	if err := json.Unmarshal(contents[parentStart:parentEnd], &parent); err != nil {
		return "", fmt.Errorf("decode hook matcher: %w", err)
	}
	if parent.Matcher == nil {
		return "", nil
	}
	return *parent.Matcher, nil
}

// hookEntryBounds returns the span to cut so that the JSON object enclosing
// position pos disappears from its array without leaving a dangling comma: the
// object together with the comma before it, or, for the first element, the
// comma and whitespace after it.
func hookEntryBounds(contents []byte, pos int) (int, int, bool) {
	objectStart, objectEnd, ok := enclosingObject(contents, pos)
	if !ok {
		return 0, 0, false
	}
	before := objectStart - 1
	for before >= 0 && isJSONSpace(contents[before]) {
		before--
	}
	if before >= 0 && contents[before] == ',' {
		return before, objectEnd, true
	}
	after := objectEnd
	for after < len(contents) && isJSONSpace(contents[after]) {
		after++
	}
	if after < len(contents) && contents[after] == ',' {
		after++
		for after < len(contents) && isJSONSpace(contents[after]) {
			after++
		}
		return objectStart, after, true
	}
	return objectStart, objectEnd, true
}

// enclosingObject returns the bounds of the innermost JSON object that contains
// position pos, scanning from the start of the document so string contents
// never pass for structure.
func enclosingObject(contents []byte, pos int) (int, int, bool) {
	var open []int
	for i := 0; i < pos && i < len(contents); i++ {
		switch contents[i] {
		case '"':
			end, ok := jsonStringEnd(contents, i)
			if !ok {
				return 0, 0, false
			}
			i = end - 1
		case '{', '[':
			open = append(open, i)
		case '}', ']':
			if len(open) == 0 {
				return 0, 0, false
			}
			open = open[:len(open)-1]
		}
	}
	objectStart := -1
	for i := len(open) - 1; i >= 0; i-- {
		if contents[open[i]] == '{' {
			objectStart = open[i]
			break
		}
	}
	if objectStart < 0 {
		return 0, 0, false
	}
	depth := 0
	for i := objectStart; i < len(contents); i++ {
		switch contents[i] {
		case '"':
			end, ok := jsonStringEnd(contents, i)
			if !ok {
				return 0, 0, false
			}
			i = end - 1
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				return objectStart, i + 1, true
			}
		}
	}
	return 0, 0, false
}

func isJSONSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// ContainsOwnedCommand reports whether a settings file already carries at least
// one hook command baum recognises. Presence of the file is not enough:
// a repository's own `.cursor/hooks.json` is not a set-up agent.
func ContainsOwnedCommand(contents []byte) bool {
	offset := 0
	for offset < len(contents) {
		match := commandField.FindIndex(contents[offset:])
		if match == nil {
			return false
		}
		start := offset + match[1] - 1
		end, ok := jsonStringEnd(contents, start)
		if !ok {
			return false
		}
		var command string
		if err := json.Unmarshal(contents[start:end], &command); err != nil {
			return false
		}
		if _, _, owned := OwnedCommand(command); owned {
			return true
		}
		offset = end
	}
	return false
}

// OwnedCommand reports the agent event a hook command runs, matching only
// Entire's own generated command and the dispatcher baum writes in its
// place, including the pre-rename dispatcher, so setup and onboarding replace
// it rather than adding a second hook beside it. A repository that wraps a
// hook in work of its own does not match: rewriting it to the bare dispatcher
// would silently drop that work, so setup
// leaves such an entry alone even though it does mention `entire hooks`.
func OwnedCommand(command string) (Spec, string, bool) {
	for _, spec := range specs {
		for _, event := range spec.Events {
			if strings.HasPrefix(command, baumPrefix(spec, event)) ||
				strings.HasPrefix(command, legacyPrefix(spec, event)) {
				return spec, event, true
			}
			if isEntireCommand(command, spec, event) {
				return spec, event, true
			}
		}
	}
	return Spec{}, "", false
}

// isEntireCommand reports whether a command is the one `entire enable`
// generates for this event. Everything but the branch taken when Entire is
// missing has to match exactly, so a later Entire's reworded install notice is
// still recognised while a command that merely mentions `entire hooks` is not.
func isEntireCommand(command string, spec Spec, event string) bool {
	prefix := "sh -c '" + entireHookGuard
	suffix := entireTail(spec, event) + "'"
	return len(command) > len(prefix)+len(suffix) &&
		strings.HasPrefix(command, prefix) &&
		strings.HasSuffix(command, suffix)
}

// jsonStringEnd returns the offset just past the JSON string literal starting
// at contents[start].
func jsonStringEnd(contents []byte, start int) (int, bool) {
	for i := start + 1; i < len(contents); i++ {
		switch contents[i] {
		case '\\':
			i++
		case '"':
			return i + 1, true
		}
	}
	return 0, false
}

// EncodeJSON encodes without Go's HTML escaping, which would otherwise replace
// the shell redirections in a dispatcher command with > sequences.
func EncodeJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, fmt.Errorf("encode JSON: %w", err)
	}
	return bytes.TrimRight(buffer.Bytes(), "\n"), nil
}
