package repos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/baum-ai/baum/agent/prompt"
)

// PromptEntry is one catalogue prompt as the app shows it in a scope: the
// global one, or one repository's (then Source and InheritedText are set).
type PromptEntry struct {
	ID             string             `json:"id"`
	Title          string             `json:"title"`
	Group          string             `json:"group"`
	UsedBy         string             `json:"used_by"`
	DefaultText    string             `json:"default_text"`
	DefaultVersion string             `json:"default_version"`
	CustomText     *string            `json:"custom_text"`
	BasedOn        *string            `json:"based_on"`
	Outdated       bool               `json:"outdated"`
	SafetyRules    []PromptSafetyRule `json:"safety_rules"`
	MissingRules   []string           `json:"missing_safety_rules"`
	Source         PromptSource       `json:"source,omitempty"`
	InheritedText  *string            `json:"inherited_text,omitempty"`
}

// PromptSafetyRule is a safety rule of a prompt, for display.
type PromptSafetyRule struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Text  string `json:"text"`
}

// PromptEntries builds the entries of a scope ("" is global), one per
// catalogue id in catalogue order. custom_text, based_on and outdated come
// from that scope's own rows. For a repository, inherited_text is the global
// override's text, else the built-in text, and source says which text a run
// uses. The repository must be picked (ErrNotPicked).
func (r *Registry) PromptEntries(ctx context.Context, repository string) ([]PromptEntry, error) {
	var sources map[prompt.ID]PromptSource
	var global map[prompt.ID]PromptOverride
	if repository != GlobalPrompts {
		_, picked, e := r.RepositorySettings(ctx, repository)
		if e != nil {
			return nil, e
		}
		if !picked {
			return nil, ErrNotPicked
		}
		if global, e = r.PromptOverrides(ctx, GlobalPrompts); e != nil {
			return nil, e
		}
		if _, sources, e = r.ResolvePrompts(ctx, repository); e != nil {
			return nil, e
		}
	}
	own, e := r.PromptOverrides(ctx, repository)
	if e != nil {
		return nil, e
	}
	out := []PromptEntry{}
	for _, c := range prompt.Catalogue() {
		entry := PromptEntry{
			ID: string(c.ID), Title: c.Title, Group: c.Group, UsedBy: c.UsedBy,
			DefaultText: c.Default, DefaultVersion: c.DefaultVersion,
			SafetyRules: []PromptSafetyRule{},
		}
		for _, rule := range c.SafetyRules {
			entry.SafetyRules = append(entry.SafetyRules, PromptSafetyRule{ID: rule.ID, Label: rule.Label, Text: rule.Text})
		}
		effective := c.Default
		if repository != GlobalPrompts {
			inherited := c.Default
			if g, ok := global[c.ID]; ok {
				inherited = g.Text
			}
			entry.InheritedText, entry.Source = &inherited, sources[c.ID]
			effective = inherited
		}
		if o, ok := own[c.ID]; ok {
			text, based := o.Text, o.BasedOn
			entry.CustomText, entry.BasedOn = &text, &based
			entry.Outdated = based != c.DefaultVersion
			effective = text
		}
		entry.MissingRules = append([]string{}, prompt.MissingSafetyRules(c.ID, effective)...)
		out = append(out, entry)
	}
	return out, nil
}

// maxPromptBody bounds a PUT body: twice the longest text, since JSON
// escaping can lengthen it, plus room for the envelope. A longer text that
// fits is refused by prompt.Validate.
const maxPromptBody = 2*prompt.MaxOverrideBytes + 1<<10

func writePromptJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writePromptError(w http.ResponseWriter, status int, message string) {
	writePromptJSON(w, status, map[string]string{"message": message})
}

// writePromptFailure answers an error of the prompt operations.
func writePromptFailure(w http.ResponseWriter, e error) {
	switch {
	case errors.Is(e, ErrNotPicked):
		writePromptError(w, http.StatusNotFound, "repository is not picked")
	case errors.Is(e, ErrInvalidPrompt):
		writePromptError(w, http.StatusBadRequest, strings.TrimPrefix(e.Error(), ErrInvalidPrompt.Error()+": "))
	default:
		writePromptError(w, http.StatusInternalServerError, http.StatusText(http.StatusInternalServerError))
	}
}

// ServePrompts answers GET .../prompts for a scope ("" is global) with
// {"prompts": [...]}; an unpicked repository is a 404.
func (r *Registry) ServePrompts(w http.ResponseWriter, req *http.Request, repository string) {
	entries, e := r.PromptEntries(req.Context(), repository)
	if e != nil {
		writePromptFailure(w, e)
		return
	}
	writePromptJSON(w, http.StatusOK, map[string][]PromptEntry{"prompts": entries})
}

// ServePromptPut answers PUT .../prompts/{id} for a scope ("" is global): it
// stores the text, or resets the prompt for null or "", and answers the
// updated entry. An unknown id and an unpicked repository are 404, a body
// that is too large 413, a malformed one (unknown or missing fields,
// trailing data) or an invalid text 400, each with {"message": ...}.
func (r *Registry) ServePromptPut(w http.ResponseWriter, req *http.Request, repository, id string) {
	if _, ok := prompt.Lookup(prompt.ID(id)); !ok {
		writePromptError(w, http.StatusNotFound, "unknown prompt")
		return
	}
	req.Body = http.MaxBytesReader(w, req.Body, maxPromptBody)
	raw, e := io.ReadAll(req.Body)
	var tooLarge *http.MaxBytesError
	if errors.As(e, &tooLarge) {
		writePromptError(w, http.StatusRequestEntityTooLarge, "the request is too large")
		return
	}
	var fields map[string]json.RawMessage
	var input struct {
		Text *string `json:"text"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e != nil || json.Unmarshal(raw, &fields) != nil || fields["text"] == nil ||
		d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
		writePromptError(w, http.StatusBadRequest, `invalid JSON request: expected {"text": string|null}`)
		return
	}
	if input.Text == nil || *input.Text == "" {
		e = r.DeletePromptOverride(req.Context(), repository, prompt.ID(id))
	} else {
		_, e = r.SetPromptOverride(req.Context(), repository, prompt.ID(id), *input.Text)
	}
	if e != nil {
		writePromptFailure(w, e)
		return
	}
	entries, e := r.PromptEntries(req.Context(), repository)
	if e != nil {
		writePromptFailure(w, e)
		return
	}
	for _, entry := range entries {
		if entry.ID == id {
			writePromptJSON(w, http.StatusOK, entry)
			return
		}
	}
	writePromptError(w, http.StatusNotFound, "unknown prompt")
}
