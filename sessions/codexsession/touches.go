package codexsession

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/lastpersonlabs/baum/sessions/codextranscript"
	"github.com/lastpersonlabs/baum/sessions/session"
	"github.com/lastpersonlabs/baum/sessions/traceprocessing"
)

var (
	hunkLine = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)
)

func (f *threadState) addTouchesFromTool(evidence *toolEvidence) {
	use := evidence.use
	if !use.Verb.FileVerb() {
		return
	}
	kind := session.TouchEdit
	if use.Verb == session.VerbRead {
		kind = session.TouchRead
	}
	paths := structuredPaths(use.Name, use.Input)
	for _, path := range paths {
		f.addTouch(evidence.callLine, use, nil, path, kind, "")
	}
}

func structuredPaths(name string, input json.RawMessage) []string {
	var object map[string]json.RawMessage
	if json.Unmarshal(input, &object) == nil {
		var out []string
		for _, key := range []string{"file_path", "notebook_path", "path"} {
			var path string
			if json.Unmarshal(object[key], &path) == nil && path != "" {
				out = append(out, path)
			}
		}
		var patch string
		_ = json.Unmarshal(object["patch"], &patch)
		out = append(out, pathsFromPatch(patch)...)
		return uniqueStrings(out)
	}
	if strings.EqualFold(name, "apply_patch") {
		var patch string
		if json.Unmarshal(input, &patch) == nil {
			return uniqueStrings(pathsFromPatch(patch))
		}
	}
	return nil
}

func pathsFromPatch(patch string) []string {
	var out []string
	for _, path := range traceprocessing.PatchHeaderPaths(patch) {
		out = append(out, strings.TrimSpace(path))
	}
	return out
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

func (f *threadState) addTouch(line codextranscript.Line, use *session.ToolUse, input *session.UserInput, path string, kind session.TouchKind, diff string) {
	if path == "" {
		return
	}
	owner := input
	var toolID *string
	key := fmt.Sprintf("orphan:%d:%s", line.Ordinal, path)
	if use != nil {
		owner = f.inputByID(use.UserInputID)
		toolID = &use.ID
		key = use.ID + "\x00" + path
	}
	if owner == nil {
		return
	}
	if existing := f.touchByToolPath[key]; existing != nil {
		if diff != "" {
			existing.Hunks = parseHunks(diff)
		}
		return
	}
	row := &session.FileTouch{
		ID: f.ids.Mint("file_touch", key), ToolUseID: toolID, UserInputID: owner.ID,
		SessionID: f.session.ID, Path: path, Kind: kind, Hunks: parseHunks(diff),
	}
	f.touches = append(f.touches, row)
	f.touchByToolPath[key] = row
	f.vendorRow(row.ID, session.EntityFileTouch, "rollout.line.path",
		fmt.Sprintf("%s:%d:%s", f.meta.ID, line.Ordinal, path), line.Ordinal)
}

func (f *threadState) inputByID(id string) *session.UserInput {
	for _, input := range f.inputs {
		if input.ID == id {
			return input
		}
	}
	return nil
}

func parseHunks(diff string) []session.Hunk {
	if diff == "" {
		return nil
	}
	var out []session.Hunk
	var current *session.Hunk
	for _, line := range strings.Split(diff, "\n") {
		match := hunkLine.FindStringSubmatch(line)
		if match != nil {
			out = append(out, session.Hunk{
				OldStart: number(match[1]), OldLines: numberDefault(match[2], 1),
				NewStart: number(match[3]), NewLines: numberDefault(match[4], 1),
			})
			current = &out[len(out)-1]
			continue
		}
		if current != nil {
			current.Lines = append(current.Lines, line)
		}
	}
	return out
}

func number(value string) int {
	n, _ := strconv.Atoi(value)
	return n
}

func numberDefault(value string, fallback int) int {
	if value == "" {
		return fallback
	}
	return number(value)
}
