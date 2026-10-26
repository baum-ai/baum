package prompt

import (
	_ "embed"
	"strings"
)

// FixVersion is the version of the fix instructions, which change with the
// text of fix-public-v1.md, fix-conflicts.md, fix-checks.md and
// fix-threads.md.
const FixVersion = "fix-public-v1"

//go:embed fix-public-v1.md
var fixPublic string

//go:embed fix-conflicts.md
var fixConflicts string

//go:embed fix-checks.md
var fixChecks string

//go:embed fix-threads.md
var fixThreads string

// Fix returns the instructions of a fix an agent makes with write access to a
// pull request's checkout (agent.Fixer): the shared rules, then the sections
// for what it fixes: merge conflicts, failing checks, review comments. The
// caller appends the data the sections refer to, delimited as untrusted.
func Fix(conflicts, checks, threads bool) string {
	parts := []string{strings.TrimRight(fixPublic, "\n")}
	if conflicts {
		parts = append(parts, strings.TrimRight(fixConflicts, "\n"))
	}
	if checks {
		parts = append(parts, strings.TrimRight(fixChecks, "\n"))
	}
	if threads {
		parts = append(parts, strings.TrimRight(fixThreads, "\n"))
	}
	return strings.Join(parts, "\n\n") + "\n"
}
