package decisionlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/baum-ai/baum/agent"
)

type adaptiveDecision struct {
	Decision
	Source *SourceContext `json:"source,omitempty"`
}

type adaptiveDecisionEvidence struct {
	Decisions   []adaptiveDecision `json:"decisions"`
	Truncated   bool               `json:"truncated"`
	Limitations []string           `json:"limitations"`
}

// Captured evidence is snapshotted on request, never silently refreshed on
// resume. It includes the extraction's quote and turn citations, not transcript
// prompts. Commit links establish relevance; they do not assert attribution.
func (b *adaptiveRun) decisions(ctx context.Context, numbers []int) error {
	for _, number := range numbers {
		if _, seen := b.state.Decisions[number]; seen {
			continue
		}
		p := b.state.Catalog[number]
		commits := []string{p.HeadSHA}
		for _, c := range b.state.Code[number].Commits {
			commits = append(commits, c.SHA)
		}
		slices.Sort(commits)
		commits = slices.Compact(commits)
		keys := []SessionKey{}
		evidence := adaptiveDecisionEvidence{Decisions: []adaptiveDecision{}, Limitations: []string{"Only already-captured sessions linked to pinned PR commits are included; missing commit links or uncaptured sessions remain unknown.", "Evidence contains extracted quotes and cited turn ordinals; complete transcripts are not loaded. Commit linkage alone does not prove feature attribution."}}
		for _, sha := range commits {
			found, err := collect(func(rows *sql.Rows) (SessionKey, error) {
				var key SessionKey
				err := rows.Scan(&key.Agent, &key.ID)
				return key, err
			})(b.w.Store.db.QueryContext(ctx, `SELECT DISTINCT cs.agent,cs.session_id FROM session_commit sc JOIN capture_session cs ON cs.agent=sc.agent AND cs.session_id=sc.session_id WHERE sc.repository=? AND sc.sha=? AND cs.repository=? ORDER BY cs.agent,cs.session_id LIMIT 51`, b.repository, sha, b.repository))
			if err != nil {
				return err
			}
			for _, key := range found {
				if !slices.Contains(keys, key) {
					if len(keys) == 50 {
						evidence.Truncated = true
						break
					}
					keys = append(keys, key)
				}
			}
		}
		for _, key := range keys {
			current, err := b.w.Store.Current(ctx, key)
			if err != nil {
				return err
			}
			for _, d := range current {
				if len(evidence.Decisions) == 200 {
					evidence.Truncated = true
					break
				}
				var source *SourceContext
				var raw string
				err := b.w.Store.db.QueryRowContext(ctx, `SELECT source_json FROM decision_source WHERE decision_id=?`, d.ID).Scan(&raw)
				if err == nil {
					source = &SourceContext{}
					if err = json.Unmarshal([]byte(raw), source); err != nil {
						return err
					}
				} else if !errors.Is(err, sql.ErrNoRows) {
					return err
				}
				evidence.Decisions = append(evidence.Decisions, adaptiveDecision{d, source.provenance()})
			}
		}
		data, err := json.Marshal(evidence)
		if err != nil {
			return err
		}
		data, err = agent.RedactTaskOutput(ctx, b.w.redactor, data, &agent.Outcome{})
		if err != nil {
			return err
		}
		if err = json.Unmarshal(data, &evidence); err != nil {
			return err
		}
		b.state.Decisions[number] = evidence
	}
	return nil
}
