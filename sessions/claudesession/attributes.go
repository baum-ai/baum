package claudesession

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/baum-ai/baum/sessions/traceprocessing"

	"github.com/baum-ai/baum/sessions/claudetranscript"
	"github.com/baum-ai/baum/sessions/session"
)

type pendingAttribute struct {
	key     string
	value   json.RawMessage
	ordinal int
	at      *time.Time
	after   *time.Time
}

type controlHandler func(*fileState, claudetranscript.Line) error

func (f *fileState) controlLine(line claudetranscript.Line) error {
	before := len(f.pending)
	defer func() {
		if len(f.pending) > before {
			f.b.report.Lines.AttributeBearing++
		}
	}()

	handler, ok := controlHandlers[line.Type]
	if !ok {
		f.b.report.discard("control/" + line.Type)
		return nil
	}
	return handler(f, line)
}

// controlHandlers maps Claude control line types to how we record them.
// String-valued controls use controlString; the rest have their own handlers.
var controlHandlers = map[string]controlHandler{
	"mode":            controlString("mode", func(c claudetranscript.Control) string { return c.Mode }),
	"permission-mode": controlString("permissionMode", func(c claudetranscript.Control) string { return c.PermissionMode }),
	"ai-title":        (*fileState).aiTitleAttribute,
	"custom-title":    (*fileState).aiTitleAttribute,
	"last-prompt":     controlString("leafUuid", func(c claudetranscript.Control) string { return c.LeafUUID }),
	// lastSequenceNum is bridge bookkeeping; only the id is worth logging.
	"bridge-session":  controlString("bridgeSessionId", func(c claudetranscript.Control) string { return c.BridgeSessionID }),
	"queue-operation": (*fileState).queueOperation,
	"agent-name":      (*fileState).rawControlAttribute,
	"relocated":       (*fileState).rawControlAttribute,
	"worktree-state":  (*fileState).rawControlAttribute,
	"pr-link":         (*fileState).rawControlAttribute,
}

func controlString(key string, get func(claudetranscript.Control) string) controlHandler {
	return func(f *fileState, line claudetranscript.Line) error {
		f.attribute(line, key, get(line.Control))
		return nil
	}
}

func (f *fileState) aiTitleAttribute(line claudetranscript.Line) error {
	title := line.Control.AITitle
	f.attribute(line, "aiTitle", title)
	if title != "" {
		f.title = title
	}
	return nil
}

func (f *fileState) rawControlAttribute(line claudetranscript.Line) error {
	value := line.Control.Value
	if len(value) == 0 || string(value) == "{}" {
		f.b.report.discard("control/" + line.Type + " (no value)")
		return nil
	}
	f.attributeValue(line, line.Type, value)
	return nil
}

// attribute buffers a string-valued attribute change.
func (f *fileState) attribute(line claudetranscript.Line, key, value string) {
	if value == "" {
		f.b.report.discard("control/" + line.Type + " (no value)")
		return
	}
	f.attributeValue(line, key, jsonString(value))
}

func (f *fileState) attributeValue(line claudetranscript.Line, key string, value json.RawMessage) {
	f.pending = append(f.pending, pendingAttribute{
		key:     key,
		value:   value,
		ordinal: line.Ordinal,
		at:      line.Timestamp,
		after:   f.lastEnvelopeTime,
	})
}

func (f *fileState) resolveAttributes() {
	latest := map[string]json.RawMessage{}
	last := map[string]int64{}
	taken := map[string]map[int64]bool{}

	for _, pending := range f.pending {
		if current, seen := latest[pending.key]; seen && string(current) == string(pending.value) {
			continue
		}
		latest[pending.key] = pending.value

		at := f.attributeTime(pending)
		if f.b.profile == traceprocessing.Hosted {
			// valid_from keeps microseconds, so each key's next change moves past
			// the microsecond of its last one.
			if previous, ok := last[pending.key]; ok && at.UnixMicro() <= previous {
				at = at.Add(time.Duration(previous-at.UnixMicro()+1) * time.Microsecond)
			}
			last[pending.key] = at.UnixMicro()
		} else {
			if taken[pending.key] == nil {
				taken[pending.key] = map[int64]bool{}
			}
			for taken[pending.key][at.UnixMicro()] {
				at = at.Add(time.Microsecond)
			}
			taken[pending.key][at.UnixMicro()] = true
		}

		row := session.SessionAttribute{
			ID:        session.MintID("session_attribute", f.sessionID, pending.key, at.Format(time.RFC3339Nano)),
			SessionID: f.sessionID,
			Key:       pending.key,
			Value:     pending.value,
			ValidFrom: at,
		}
		f.b.graph.Attributes = append(f.b.graph.Attributes, row)
		f.vendorRow(row.ID, session.EntitySessionAttribute, "ordinal",
			fmt.Sprintf("%d:%s", pending.ordinal, pending.key), pending.ordinal)
	}
}

func (f *fileState) attributeTime(pending pendingAttribute) time.Time {
	switch {
	case pending.at != nil:
		return *pending.at
	case pending.after != nil:
		return *pending.after
	case f.firstAnyTime != nil:
		return *f.firstAnyTime
	case f.times.Start != nil:
		return *f.times.Start
	default:
		return time.Unix(0, 0).UTC().Add(time.Duration(pending.ordinal) * time.Microsecond)
	}
}

func jsonString(value string) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`null`)
	}
	return raw
}
