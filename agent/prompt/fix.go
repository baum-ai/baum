package prompt

import (
	_ "embed"
	"strings"
)

// FixVersion is the version of the fix instructions, which change with the
// text of fix-public-v1.md, fix-conflicts.md, fix-checks.md and
// fix-threads.md and fix-chat.md.
const FixVersion = "fix-public-v1"

//go:embed fix-public-v1.md
var fixPublic string

//go:embed fix-conflicts.md
var fixConflicts string

//go:embed fix-checks.md
var fixChecks string

//go:embed fix-chat.md
var fixChat string

//go:embed fix-threads.md
var fixThreads string

// Fix returns the instructions of a fix an agent makes with write access to a
// pull request's checkout (agent.Fixer): the shared rules, then the sections
// for what it fixes: merge conflicts, failing checks, review comments, a
// change from a conversation. The
// caller appends the data the sections refer to, delimited as untrusted.
func Fix(conflicts, checks, threads, chat bool) string {
	text, _ := FixWith(nil, conflicts, checks, threads, chat)
	return text
}

// FixWith is Fix with Overrides: the fix, fix-conflicts, fix-checks,
// fix-threads and fix-chat entries replace the built-in text of the shared
// rules and of the sections included. The version is FixVersion, with
// CustomSuffix when an included part was overridden; an override of a section
// that is not included does not count. Callers must Validate override text
// first; FixWith does not.
func FixWith(o Overrides, conflicts, checks, threads, chat bool) (text, version string) {
	custom := false
	part := func(id ID, builtin string) string {
		body, overridden := o.text(id, builtin)
		custom = custom || overridden
		return strings.TrimRight(body, "\n")
	}
	parts := []string{part(IDFix, fixPublic)}
	if conflicts {
		parts = append(parts, part(IDFixConflicts, fixConflicts))
	}
	if checks {
		parts = append(parts, part(IDFixChecks, fixChecks))
	}
	if threads {
		parts = append(parts, part(IDFixThreads, fixThreads))
	}
	if chat {
		parts = append(parts, part(IDFixChat, fixChat))
	}
	return strings.Join(parts, "\n\n") + "\n", withCustom(FixVersion, custom)
}
