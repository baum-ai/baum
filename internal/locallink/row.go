package locallink

import (
	"time"

	"github.com/baum-ai/baum/linkage/sessioncorrelate"
	"github.com/baum-ai/baum/redact"
	"github.com/baum-ai/baum/sessions"
	"github.com/baum-ai/baum/sessions/session"
)

// Row is one session as "the local session reader" lists it, and the JSON shape of its
// --json output. Its text is redacted.
type Row struct {
	Source      string              `json:"source"`
	Vendor      string              `json:"vendor"`
	ID          string              `json:"id"`
	Title       string              `json:"title,omitempty"`
	Branch      string              `json:"branch,omitempty"`
	StartedAt   *time.Time          `json:"started_at,omitempty"`
	EndedAt     *time.Time          `json:"ended_at,omitempty"`
	Outcome     session.Outcome     `json:"outcome"`
	Commits     map[string][]string `json:"commits,omitempty"`
	EditedFiles int                 `json:"edited_files"`
	Path        string              `json:"path"`
	Notes       []string            `json:"notes,omitempty"`
}

// RowOf summarises s for a listing, redacting its title with engine.
func RowOf(engine *redact.Engine, s sessions.Session) (Row, error) {
	return rowOf(engine, s, sessioncorrelate.Correlate(s.Graph, s.Vendor))
}

func rowOf(engine *redact.Engine, s sessions.Session, correlated sessioncorrelate.Result) (Row, error) {
	facts := sessionFacts{correlated: correlated}
	if root := s.Root(); root != nil {
		facts.root = true
		facts.startedAt, facts.endedAt, facts.gitBranch = root.StartedAt, root.EndedAt, root.GitBranch
		facts.title, facts.outcome = root.Title, root.Outcome
	}
	return rowOfFacts(engine, s, facts)
}

func rowOfFacts(engine *redact.Engine, s sessions.Session, facts sessionFacts) (Row, error) {
	correlated := facts.correlated
	row := Row{Source: s.Source.String(), Vendor: s.Vendor, ID: s.ID, Outcome: session.OutcomeUnknown}
	if facts.root {
		row.Outcome = facts.outcome
		if facts.startedAt != nil {
			at := *facts.startedAt
			row.StartedAt = &at
		}
		if facts.endedAt != nil {
			at := *facts.endedAt
			row.EndedAt = &at
		}
		if facts.title != nil {
			title, _, err := engine.Redact(*facts.title)
			if err != nil {
				return Row{}, err
			}
			row.Title = title
		}
		if facts.gitBranch != nil {
			row.Branch = *facts.gitBranch
		}
	}
	if row.Branch == "" && len(correlated.Branches) > 0 {
		row.Branch = correlated.Branches[0]
	}
	if row.Branch != "" {
		branch, _, err := engine.Redact(row.Branch)
		if err != nil {
			return Row{}, err
		}
		row.Branch = branch
	}
	for _, note := range s.Notes {
		note, _, err := engine.Redact(note)
		if err != nil {
			return Row{}, err
		}
		row.Notes = append(row.Notes, note)
	}
	path, _, err := engine.Redact(s.Path)
	if err != nil {
		return Row{}, err
	}
	row.Path = path
	for _, ref := range correlated.CommitRefs {
		if row.Commits == nil {
			row.Commits = map[string][]string{}
		}
		row.Commits[string(ref.Role)] = append(row.Commits[string(ref.Role)], ref.SHA)
	}
	for _, file := range correlated.Files {
		if file.Kind == session.TouchEdit {
			row.EditedFiles++
		}
	}
	return row, nil
}
