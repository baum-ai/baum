package codexsession

import (
	"encoding/json"
	"fmt"

	"github.com/lastpersonlabs/baum/sessions/codextranscript"
	"github.com/lastpersonlabs/baum/sessions/session"
)

func (f *threadState) startBatch(line codextranscript.Line) *session.ModelInvocation {
	if f.batch != nil {
		return f.batch.row
	}
	f.batchNumber++
	locator := fmt.Sprintf("%s:%d:%d", f.openTurn, f.inputLine, f.batchNumber)
	row := &session.ModelInvocation{
		ID: f.ids.Mint("model_invocation", locator), SessionID: f.session.ID,
		UserInputID: f.activeInput.ID, StartedAt: timePtr(line.Timestamp),
	}
	if raw := f.latestContext["model"]; len(raw) > 0 {
		var model string
		if json.Unmarshal(raw, &model) == nil && model != "" {
			row.Model = &model
		}
	}
	if parent := f.lastByInput[row.UserInputID]; parent != "" {
		row.ParentModelInvocationID = &parent
	}
	f.lastByInput[row.UserInputID] = row.ID
	f.invocations = append(f.invocations, row)
	f.batch = &batchState{row: row}
	turn := f.turn(f.openTurn)
	turn.unaccounted = append(turn.unaccounted, row)
	f.vendorRow(row.ID, session.EntityModelInvocation, "turn_id.invocation", locator, line.Ordinal)
	return row
}
