package codexsession

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/lastpersonlabs/baum/sessions/codextranscript"
	"github.com/lastpersonlabs/baum/sessions/session"
)

func (f *threadState) applyContext(line codextranscript.Line) {
	fields := line.TurnContext.AttributeFields()
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := fields[key]
		f.latestContext[key] = append(json.RawMessage(nil), value...)
		f.addAttribute(line, key, value)
	}
	if f.workingDir == "" {
		f.workingDir = line.TurnContext.CWD()
	}
}

func (f *threadState) applySessionMeta(line codextranscript.Line) {
	if line.SessionMeta == nil || line.SessionMeta.Git == nil {
		return
	}
	git := line.SessionMeta.Git
	if git.CommitHash != "" {
		raw, _ := json.Marshal(git.CommitHash)
		f.addAttribute(line, "git.commit_hash", raw)
	}
	if git.Branch != "" {
		raw, _ := json.Marshal(git.Branch)
		f.addAttribute(line, "git.branch", raw)
	}
	if git.RepositoryURL != "" {
		raw, _ := json.Marshal(git.RepositoryURL)
		f.addAttribute(line, "git.repository_url", raw)
	}
}

func (f *threadState) addAttribute(line codextranscript.Line, key string, value json.RawMessage) {
	if len(value) == 0 {
		value = json.RawMessage("null")
	}
	if current, ok := f.latestAttribute[key]; ok && bytes.Equal(current, value) {
		return
	}
	f.latestAttribute[key] = append(json.RawMessage(nil), value...)
	at := line.Timestamp.UTC().Truncate(time.Microsecond)
	if previous, ok := f.attributeTime[key]; ok && !at.After(previous) {
		at = previous.Add(time.Microsecond)
	}
	f.attributeTime[key] = at
	locator := fmt.Sprintf("%s:%d:%s", f.meta.ID, line.Ordinal, key)
	row := &session.SessionAttribute{
		ID:        f.ids.Mint("session_attribute", fmt.Sprintf("%d", line.Ordinal), key),
		SessionID: f.session.ID, Key: key, Value: append(json.RawMessage(nil), value...), ValidFrom: at,
	}
	f.attributes = append(f.attributes, row)
	f.vendorRow(row.ID, session.EntitySessionAttribute, "rollout.line.key", locator, line.Ordinal)
}
