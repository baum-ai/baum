package uiserver

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/themes"
)

type appearanceSettings struct {
	Theme        string `json:"theme"`
	KeyHints     bool   `json:"key_hints"`
	PageModifier string `json:"page_modifier"`
	Sidebar      string `json:"sidebar"`
	ReviewPage   string `json:"review_page"`
	HowItWorks   string `json:"how_it_works"`
	// ArrangedTiles says a version whose tiles were arranged (it has a
	// composition) opens on the tiles page: tile reviews are on, which turns
	// it on, and the review page is not tiles already. ?view=full in an
	// address still opens the full review. It is read only.
	ArrangedTiles bool              `json:"arranged_tiles"`
	LightTheme    string            `json:"light_theme"`
	DarkTheme     string            `json:"dark_theme"`
	Themes        []appearanceTheme `json:"themes"`
}

// appearanceTheme is a colour theme as the app lists it: the built-ins first
// (themes.Builtins, with their tokens), then the person's own.
type appearanceTheme struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Base    string            `json:"base"`
	Builtin bool              `json:"builtin,omitempty"`
	Tokens  map[string]string `json:"tokens"`
}

func appearanceOf(p repos.Preferences) appearanceSettings {
	theme := p.Appearance.Theme
	if theme == "" {
		theme = "system"
	}
	sidebar := p.Appearance.Sidebar
	if sidebar != "folded" {
		sidebar = "pinned"
	}
	reviewPage := p.Appearance.ReviewPage
	if reviewPage != "tiles" {
		reviewPage = "full"
	}
	howItWorks := p.Appearance.HowItWorks
	if howItWorks != "one_page" {
		howItWorks = "pages"
	}
	list := make([]appearanceTheme, 0, len(themes.Builtins)+len(p.Appearance.Themes))
	for _, t := range themes.Builtins {
		tokens := map[string]string{}
		maps.Copy(tokens, t.Tokens)
		list = append(list, appearanceTheme{ID: t.ID, Name: t.Name, Base: t.Base, Builtin: true, Tokens: tokens})
	}
	for _, t := range p.Appearance.Themes {
		tokens := t.Tokens
		if tokens == nil {
			tokens = map[string]string{}
		}
		list = append(list, appearanceTheme{ID: t.ID, Name: t.Name, Base: t.Base, Tokens: tokens})
	}
	slot := func(id, fallback string) string {
		if id != "" && slices.ContainsFunc(list, func(t appearanceTheme) bool { return t.ID == id }) {
			return id
		}
		return fallback
	}
	return appearanceSettings{
		Theme:         theme,
		KeyHints:      p.Appearance.KeyHints == nil || *p.Appearance.KeyHints,
		PageModifier:  p.Appearance.PageModifier,
		Sidebar:       sidebar,
		ReviewPage:    reviewPage,
		HowItWorks:    howItWorks,
		ArrangedTiles: p.Labs.TileReviewsOn() && reviewPage != "tiles",
		LightTheme:    slot(p.Appearance.LightTheme, themes.DefaultLight),
		DarkTheme:     slot(p.Appearance.DarkTheme, themes.DefaultDark),
		Themes:        list,
	}
}

func (a LocalAPI) getAppearance(w http.ResponseWriter, r *http.Request) {
	prefs, err := a.Registry.Preferences(r.Context())
	if err != nil {
		http.Error(w, "Could not read appearance settings", http.StatusInternalServerError)
		return
	}
	writeJSON(w, appearanceOf(prefs))
}

// appearanceInput is the body of PUT /api/settings/appearance: each field set
// is a change.
type appearanceInput struct {
	Theme        *string `json:"theme,omitempty"`
	KeyHints     *bool   `json:"key_hints,omitempty"`
	PageModifier *string `json:"page_modifier,omitempty"`
	Sidebar      *string `json:"sidebar,omitempty"`
	ReviewPage   *string `json:"review_page,omitempty"`
	HowItWorks   *string `json:"how_it_works,omitempty"`
	LightTheme   *string `json:"light_theme,omitempty"`
	DarkTheme    *string `json:"dark_theme,omitempty"`
}

// check refuses what the route refuses (400).
func (in appearanceInput) check() error {
	if in.Theme != nil && *in.Theme != "system" && *in.Theme != "light" && *in.Theme != "dark" {
		return errors.New("theme must be system, light or dark")
	}
	if in.PageModifier != nil && *in.PageModifier != "" && *in.PageModifier != "meta" &&
		*in.PageModifier != "ctrl" && *in.PageModifier != "alt" {
		return errors.New("page_modifier must be empty, meta, ctrl or alt")
	}
	if in.Sidebar != nil && *in.Sidebar != "pinned" && *in.Sidebar != "folded" {
		return errors.New("sidebar must be pinned or folded")
	}
	if in.ReviewPage != nil && *in.ReviewPage != "full" && *in.ReviewPage != "tiles" {
		return errors.New("review_page must be full or tiles")
	}
	if in.HowItWorks != nil && *in.HowItWorks != "pages" && *in.HowItWorks != "one_page" {
		return errors.New("how_it_works must be pages or one_page")
	}
	return nil
}

// checkThemes refuses a theme slot that names a theme the saved preferences
// do not have (400). Empty is the slot's default.
func (in appearanceInput) checkThemes(p repos.Preferences) error {
	for slot, id := range map[string]*string{"light_theme": in.LightTheme, "dark_theme": in.DarkTheme} {
		if id != nil && !themeKnown(p, *id) {
			return themeInvalid("%s names an unknown theme", slot)
		}
	}
	return nil
}

// themeKnown reports whether id is empty, a built-in or a custom theme in p.
func themeKnown(p repos.Preferences, id string) bool {
	return id == "" || themes.IsBuiltin(id) ||
		slices.ContainsFunc(p.Appearance.Themes, func(t repos.CustomTheme) bool { return t.ID == id })
}

// edit is the route's change of the saved preferences.
func (in appearanceInput) edit(p repos.Preferences) repos.Preferences {
	if in.Theme != nil {
		p.Appearance.Theme = *in.Theme
	}
	if in.KeyHints != nil {
		p.Appearance.KeyHints = in.KeyHints
	}
	if in.PageModifier != nil {
		p.Appearance.PageModifier = *in.PageModifier
	}
	if in.Sidebar != nil {
		p.Appearance.Sidebar = *in.Sidebar
	}
	if in.ReviewPage != nil {
		p.Appearance.ReviewPage = *in.ReviewPage
	}
	if in.HowItWorks != nil {
		p.Appearance.HowItWorks = *in.HowItWorks
	}
	if in.LightTheme != nil {
		p.Appearance.LightTheme = *in.LightTheme
	}
	if in.DarkTheme != nil {
		p.Appearance.DarkTheme = *in.DarkTheme
	}
	return p
}

func (a LocalAPI) putAppearance(w http.ResponseWriter, r *http.Request) {
	var input appearanceInput
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid JSON request", http.StatusBadRequest)
		return
	}
	if err := input.check(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	prefs, err := a.updatePreferences(r.Context(), input.checkThemes, input.edit)
	if err != nil {
		a.themeError(w, err, "Could not save appearance settings")
		return
	}
	writeJSON(w, appearanceOf(prefs))
}

// errThemeNotFound reports a custom theme id the preferences do not hold.
var errThemeNotFound = errors.New("no such theme")

// errThemeInvalid is a refusal of the request itself (400).
type errThemeInvalid struct{ error }

func themeInvalid(format string, args ...any) error {
	return errThemeInvalid{fmt.Errorf(format, args...)}
}

// themeError answers what a theme change returned: a refusal is 400, a
// missing theme 404, anything else a failed save.
func (a LocalAPI) themeError(w http.ResponseWriter, err error, failure string) {
	var invalid errThemeInvalid
	switch {
	case errors.As(err, &invalid):
		http.Error(w, invalid.Error(), http.StatusBadRequest)
	case errors.Is(err, errThemeNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	default:
		http.Error(w, failure, http.StatusInternalServerError)
	}
}

// slotOf validates a theme slot: "light", "dark" or empty for neither.
func slotOf(slot string) error {
	if slot != "" && slot != "light" && slot != "dark" {
		return themeInvalid("slot must be light, dark or empty")
	}
	return nil
}

// cleanTheme validates t and returns it as it is stored: the name trimmed and
// the colours without surrounding whitespace, as the validator read them.
func cleanTheme(t repos.CustomTheme) (repos.CustomTheme, error) {
	if err := themes.Validate(themes.Theme{ID: t.ID, Name: t.Name, Base: t.Base, Tokens: t.Tokens}); err != nil {
		return t, errThemeInvalid{err}
	}
	t.Name = trimName(t.Name)
	trimmed := make(map[string]string, len(t.Tokens))
	for token, value := range t.Tokens {
		trimmed[token] = strings.Trim(value, " \t\n\r\f")
	}
	t.Tokens = trimmed
	return t, nil
}

// addTheme adds t, whose ID the caller chose, to p's custom themes and puts it
// in the slot named ("light", "dark" or "" for neither).
func addTheme(p repos.Preferences, t repos.CustomTheme, slot string) (repos.Preferences, error) {
	if err := slotOf(slot); err != nil {
		return p, err
	}
	if len(p.Appearance.Themes) >= themes.MaxThemes {
		return p, themeInvalid("at most %d custom themes; delete one first", themes.MaxThemes)
	}
	cleaned, err := cleanTheme(t)
	if err != nil {
		return p, err
	}
	if t.ID == "" || themeKnown(p, t.ID) {
		return p, themeInvalid("theme id %q is taken", t.ID)
	}
	t = cleaned
	p.Appearance.Themes = append(slices.Clone(p.Appearance.Themes), t)
	switch slot {
	case "light":
		p.Appearance.LightTheme = t.ID
	case "dark":
		p.Appearance.DarkTheme = t.ID
	}
	return p, nil
}

// themeEdit is a change to a custom theme: its name, or its base and tokens
// (which go together), or both. A nil field is left as it is.
type themeEdit struct {
	Name   *string
	Base   *string
	Tokens map[string]string
}

// editTheme changes a custom theme in place: its id and the slots that use it
// stay. The theme as changed is validated as a new one is, with the same
// limits.
func editTheme(p repos.Preferences, id string, edit themeEdit) (repos.Preferences, error) {
	if themes.IsBuiltin(id) {
		return p, themeInvalid("built-in themes cannot be changed")
	}
	if edit.Name == nil && edit.Base == nil && edit.Tokens == nil {
		return p, themeInvalid("name, or base and tokens, is required")
	}
	if (edit.Base == nil) != (edit.Tokens == nil) {
		return p, themeInvalid("base and tokens go together")
	}
	i := slices.IndexFunc(p.Appearance.Themes, func(t repos.CustomTheme) bool { return t.ID == id })
	if i < 0 {
		return p, errThemeNotFound
	}
	t := p.Appearance.Themes[i]
	if edit.Name != nil {
		t.Name = *edit.Name
	}
	if edit.Base != nil {
		t.Base, t.Tokens = *edit.Base, edit.Tokens
	}
	t, err := cleanTheme(t)
	if err != nil {
		return p, err
	}
	list := slices.Clone(p.Appearance.Themes)
	list[i] = t
	p.Appearance.Themes = list
	return p, nil
}

// deleteTheme removes a custom theme and resets a slot that used it.
func deleteTheme(p repos.Preferences, id string) (repos.Preferences, error) {
	if themes.IsBuiltin(id) {
		return p, themeInvalid("built-in themes cannot be deleted")
	}
	i := slices.IndexFunc(p.Appearance.Themes, func(t repos.CustomTheme) bool { return t.ID == id })
	if i < 0 {
		return p, errThemeNotFound
	}
	p.Appearance.Themes = slices.Delete(slices.Clone(p.Appearance.Themes), i, i+1)
	if p.Appearance.LightTheme == id {
		p.Appearance.LightTheme = ""
	}
	if p.Appearance.DarkTheme == id {
		p.Appearance.DarkTheme = ""
	}
	return p, nil
}

func trimName(name string) string {
	return strings.TrimSpace(name)
}

// maxThemeBody is the most a themes route reads of a request.
const maxThemeBody = 16 << 10

func decodeThemeBody(w http.ResponseWriter, r *http.Request, into any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxThemeBody)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(into) != nil || d.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid JSON request", http.StatusBadRequest)
		return false
	}
	return true
}

// saveTheme saves the change the pure helper makes: the helper runs as the
// check, so a refusal writes nothing, and again as the edit.
func (a LocalAPI) saveTheme(w http.ResponseWriter, r *http.Request, change func(repos.Preferences) (repos.Preferences, error)) {
	prefs, err := a.updatePreferences(r.Context(), func(p repos.Preferences) error {
		_, err := change(p)
		return err
	}, func(p repos.Preferences) repos.Preferences {
		p, _ = change(p)
		return p
	})
	if err != nil {
		a.themeError(w, err, "Could not save appearance settings")
		return
	}
	writeJSON(w, appearanceOf(prefs))
}

// newThemeID is the id of a new custom theme.
func newThemeID() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "c-" + hex.EncodeToString(raw[:]), nil
}

// postTheme is POST /api/settings/appearance/themes: it creates a theme with
// a new id and, when slot is light or dark, puts it in that slot.
func (a LocalAPI) postTheme(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name   string            `json:"name"`
		Base   string            `json:"base"`
		Tokens map[string]string `json:"tokens"`
		Slot   string            `json:"slot"`
	}
	if !decodeThemeBody(w, r, &input) {
		return
	}
	id, err := newThemeID()
	if err != nil {
		http.Error(w, "Could not save appearance settings", http.StatusInternalServerError)
		return
	}
	theme := repos.CustomTheme{ID: id, Name: input.Name, Base: input.Base, Tokens: input.Tokens}
	a.saveTheme(w, r, func(p repos.Preferences) (repos.Preferences, error) { return addTheme(p, theme, input.Slot) })
}

// patchTheme is PATCH /api/settings/appearance/themes/{id}: it renames, or
// replaces the base and tokens (with a name or not), keeping the id and the
// slots that use the theme.
func (a LocalAPI) patchTheme(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name   *string            `json:"name"`
		Base   *string            `json:"base"`
		Tokens *map[string]string `json:"tokens"`
	}
	if !decodeThemeBody(w, r, &input) {
		return
	}
	edit := themeEdit{Name: input.Name, Base: input.Base}
	if input.Tokens != nil {
		edit.Tokens = *input.Tokens
		if edit.Tokens == nil {
			edit.Tokens = map[string]string{}
		}
	}
	id := r.PathValue("id")
	a.saveTheme(w, r, func(p repos.Preferences) (repos.Preferences, error) { return editTheme(p, id, edit) })
}

// deleteTheme is DELETE /api/settings/appearance/themes/{id}.
func (a LocalAPI) deleteTheme(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a.saveTheme(w, r, func(p repos.Preferences) (repos.Preferences, error) { return deleteTheme(p, id) })
}
