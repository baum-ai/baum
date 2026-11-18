package themes

// MinContrast is the WCAG 2 AA contrast ratio for body text.
const MinContrast = 4.5

// Warning is a pair of theme tokens whose contrast is below MinContrast.
type Warning struct {
	Foreground string  `json:"foreground"`
	Background string  `json:"background"`
	Ratio      float64 `json:"ratio"`
}

// pairs are the foreground and background tokens that must stay readable. The
// text on --action-primary is --text-inverse, which themes cannot set, so it
// follows the base's palette.
var pairs = [][2]string{
	{"--text-primary", "--surface-page"},
	{"--text-secondary", "--surface-page"},
	{"--text-primary", "--surface-card"},
	{"--text-primary", "--surface-canvas"},
	{"--text-inverse", "--action-primary"},
	{"--status-critical", "--surface-page"},
}

// Contrast returns the pairs of the theme's resolved colours whose WCAG 2
// contrast ratio is under MinContrast. Translucent colours are composited
// over the page surface. A theme that cannot be resolved has no warnings.
func Contrast(t Theme) []Warning {
	colours, err := Resolve(t.Base, t.Tokens)
	if err != nil {
		return nil
	}
	page, ok := colours["--surface-page"]
	if !ok {
		return nil
	}
	under := RGBA{1, 1, 1, 1}
	if t.Base == "dark" {
		under = RGBA{0, 0, 0, 1}
	}
	page = page.Over(under)
	var out []Warning
	for _, pair := range pairs {
		fg, fok := colours[pair[0]]
		bg, bok := colours[pair[1]]
		if !fok || !bok {
			continue
		}
		bg = bg.Over(page)
		if r := ratio(fg.Over(bg), bg); r < MinContrast {
			out = append(out, Warning{Foreground: pair[0], Background: pair[1], Ratio: r})
		}
	}
	return out
}
