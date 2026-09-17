package codextranscript

import "fmt"

// ThreadClass decides whether one rollout enters a session tree.
type ThreadClass string

const (
	ThreadRoot     ThreadClass = "root"
	ThreadChild    ThreadClass = "child"
	ThreadInternal ThreadClass = "internal"
)

// ClassifyThread uses only the first session_meta.
func ClassifyThread(lines []Line) (ThreadClass, error) {
	if len(lines) == 0 || lines[0].SessionMeta == nil {
		return "", fmt.Errorf("thread does not start with session_meta")
	}
	source := lines[0].SessionMeta.Source
	switch {
	case source.ThreadSpawn != nil:
		return ThreadChild, nil
	case source.Internal != "" || source.Subagent != "" || source.Other != "":
		return ThreadInternal, nil
	default:
		return ThreadRoot, nil
	}
}
