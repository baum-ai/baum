// Package themes is the desktop app's custom colour themes: the allowlist of
// palette tokens a theme may set, the validation of a theme, and the
// evaluation of the viewer's palette recipes with a theme's tokens applied,
// which the contrast check reads.
//
// A theme is a base ("light" or "dark") and colour overrides for a few
// palette tokens. The package imports no other baum package but the tile kit
// (for the generated palette), so the registry, the app server and the agent
// tools can all use it. See the appearance section of oss/app/README.md.
package themes

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The built-in themes: the palette as it ships, which have no tokens.
const (
	DefaultLight = "default-light"
	DefaultDark  = "default-dark"
)

// Limits on a theme and on how many a person keeps.
const (
	MaxThemes    = 32
	MaxNameRunes = 60
	MaxValue     = 64
)

// Tokens is the allowlist of the palette tokens a theme may set: the seeds in
// viewer/src/palette.css and the roles derived from them that people most
// often want to set directly.
var Tokens = map[string]bool{
	"--color-ink": true, "--color-paper": true, "--color-shadow": true,
	"--brand-forest": true, "--brand-sand-100": true, "--brand-green-600": true,
	"--color-critical": true, "--color-blue": true,
	"--series-1": true, "--series-2": true, "--series-3": true, "--series-4": true,
	"--series-5": true, "--series-6": true, "--series-7": true, "--series-8": true,
	"--series-caramel": true, "--series-sage": true, "--series-espresso": true,

	"--surface-page": true, "--surface-card": true, "--surface-canvas": true, "--surface-popup": true,
	"--text-primary": true, "--text-secondary": true, "--text-inverse": true,
	"--border-default": true, "--border-focus": true,
	"--action-primary": true, "--action-primary-hover": true,
	"--nav-hover": true, "--nav-selected": true,
	"--status-positive": true, "--status-caution": true, "--status-critical": true,
}

// Seeds are the allowlisted tokens the rest of the palette derives from:
// changing one recolours everything built on it, so a theme is best made of
// seeds, with roles set only where a seed cannot reach.
var Seeds = []string{
	"--color-ink", "--color-paper", "--color-shadow",
	"--brand-forest", "--brand-sand-100", "--brand-green-600",
	"--color-critical", "--color-blue",
	"--series-1", "--series-2", "--series-3", "--series-4",
	"--series-5", "--series-6", "--series-7", "--series-8",
	"--series-caramel", "--series-sage", "--series-espresso",
}

// Roles are the allowlisted tokens that are not seeds, sorted.
func Roles() []string {
	var out []string
	for token := range Tokens {
		if !slices.Contains(Seeds, token) {
			out = append(out, token)
		}
	}
	slices.Sort(out)
	return out
}

// Theme is a custom colour theme.
type Theme struct {
	ID     string
	Name   string
	Base   string
	Tokens map[string]string
}

// Validate refuses a theme whose name, base or tokens are not acceptable. A
// token value is a plain colour (see ParseColor), so nothing that could leave
// a declaration (var(), url(), ";", "}") is accepted. A theme has no count cap
// of its own: each token is on the allowlist and a map holds one value per
// token, so the allowlist bounds it.
func Validate(t Theme) error {
	if t.Base != "light" && t.Base != "dark" {
		return errors.New("theme base must be light or dark")
	}
	name := strings.TrimSpace(t.Name)
	if n := utf8.RuneCountInString(name); n < 1 || n > MaxNameRunes {
		return fmt.Errorf("theme name must be 1 to %d characters", MaxNameRunes)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return errors.New("theme name must not contain control characters")
		}
	}
	for token, value := range t.Tokens {
		if err := ValidateToken(token, value); err != nil {
			return err
		}
	}
	return nil
}

// ValidateName refuses a name Validate would.
func ValidateName(name string) error {
	return Validate(Theme{Base: "light", Name: name})
}

// ValidateToken refuses a token outside the allowlist and a value that is not
// a colour of at most MaxValue bytes.
func ValidateToken(token, value string) error {
	if !Tokens[token] {
		return fmt.Errorf("%q is not a theme token", token)
	}
	if len(value) > MaxValue {
		return fmt.Errorf("%s must be at most %d bytes", token, MaxValue)
	}
	if _, err := ParseColor(value); err != nil {
		return fmt.Errorf("%s must be a hex, rgb(), hsl() or oklch() colour", token)
	}
	return nil
}
