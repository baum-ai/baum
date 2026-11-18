package themes

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
)

func near(a, b RGBA, tol float64) bool {
	return math.Abs(a.R-b.R) <= tol && math.Abs(a.G-b.G) <= tol && math.Abs(a.B-b.B) <= tol && math.Abs(a.A-b.A) <= tol
}

func TestParseColor(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want RGBA
	}{
		{"#fff", RGBA{1, 1, 1, 1}},
		{"#f008", RGBA{1, 0, 0, 0x88 / 255.0}},
		{"#1f1b17", RGBA{0x1f / 255.0, 0x1b / 255.0, 0x17 / 255.0, 1}},
		{"#00000080", RGBA{0, 0, 0, 0x80 / 255.0}},
		{"rgb(255, 0, 0)", RGBA{1, 0, 0, 1}},
		{"rgb(255 0 0 / 50%)", RGBA{1, 0, 0, 0.5}},
		{"rgba(0, 255, 0, 0.25)", RGBA{0, 1, 0, 0.25}},
		{"rgb(100%, 0%, 0%)", RGBA{1, 0, 0, 1}},
		{"hsl(120, 100%, 50%)", RGBA{0, 1, 0, 1}},
		{"hsl(240deg 100% 50% / 0.5)", RGBA{0, 0, 1, 0.5}},
		{"hsla(0, 0%, 100%, 1)", RGBA{1, 1, 1, 1}},
		{"oklch(1 0 0)", RGBA{1, 1, 1, 1}},
		{"oklch(0 0 0)", RGBA{0, 0, 0, 1}},
		// oklch(0.628 0.2577 29.23) is sRGB red.
		{"oklch(0.628 0.2577 29.23)", RGBA{1, 0, 0, 1}},
		{"OKLCH(62.8% 0.2577 29.23deg / 100%)", RGBA{1, 0, 0, 1}},
	} {
		got, err := ParseColor(tc.in)
		if err != nil || !near(got, tc.want, 0.01) {
			t.Errorf("ParseColor(%q) = %v, %v; want %v", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "red", "transparent", "var(--x)", "url(x)", "#12", "rgb(1,2)", "rgb(a,b,c)", "color(srgb 1 1 1)", "rgb(1 2 3))"} {
		if _, err := ParseColor(bad); err == nil {
			t.Errorf("ParseColor(%q) accepted", bad)
		}
	}
}

func TestValidateCases(t *testing.T) {
	data, err := os.ReadFile("testdata/validate.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Token string `json:"token"`
		Value string `json:"value"`
		OK    bool   `json:"ok"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 30 {
		t.Fatalf("only %d cases", len(cases))
	}
	for _, c := range cases {
		err := ValidateToken(c.Token, c.Value)
		if (err == nil) != c.OK {
			t.Errorf("%s: %q: ok=%v, got %v", c.Token, c.Value, c.OK, err)
		}
		theme := Theme{Name: "T", Base: "dark", Tokens: map[string]string{c.Token: c.Value}}
		if (Validate(theme) == nil) != c.OK {
			t.Errorf("Validate with %s: %q: ok=%v", c.Token, c.Value, c.OK)
		}
	}
}

func TestValidate(t *testing.T) {
	ok := Theme{Name: " Dusk ", Base: "dark", Tokens: map[string]string{"--surface-page": "#101820"}}
	if err := Validate(ok); err != nil {
		t.Fatal(err)
	}
	many := map[string]string{}
	for token := range Tokens {
		many[token] = "#000"
	}
	if err := Validate(Theme{Name: "all", Base: "light", Tokens: many}); err != nil {
		t.Fatalf("every allowlisted token: %v", err)
	}
	for name, theme := range map[string]Theme{
		"base":    {Name: "x", Base: "sepia"},
		"no base": {Name: "x"},
		"empty":   {Name: "   ", Base: "light"},
		"long":    {Name: strings.Repeat("a", 61), Base: "light"},
		"control": {Name: "a\nb", Base: "light"},
		"tab":     {Name: "a\tb", Base: "light"},
		"too many": {Name: "x", Base: "light", Tokens: func() map[string]string {
			m := map[string]string{}
			for i := 0; i <= MaxTokens; i++ {
				m["--t"+string(rune('a'+i%26))+string(rune('a'+i/26))] = "#fff"
			}
			return m
		}()},
		"not token": {Name: "x", Base: "light", Tokens: map[string]string{"--font-text": "#fff"}},
	} {
		if Validate(theme) == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := Validate(Theme{Name: strings.Repeat("é", 60), Base: "light"}); err != nil {
		t.Errorf("60 runes: %v", err)
	}
}

func TestAllowlistTokensExist(t *testing.T) {
	light, _ := Resolve("light", nil)
	dark, _ := Resolve("dark", nil)
	for token := range Tokens {
		if _, ok := light[token]; !ok {
			t.Errorf("%s does not resolve in the light palette", token)
		}
		if _, ok := dark[token]; !ok {
			t.Errorf("%s does not resolve in the dark palette", token)
		}
	}
}

func TestResolveDefaults(t *testing.T) {
	light, err := Resolve("light", nil)
	if err != nil {
		t.Fatal(err)
	}
	dark, err := Resolve("dark", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !near(light["--surface-page"], RGBA{1, 1, 1, 1}, 0.001) {
		t.Errorf("light page: %v", light["--surface-page"])
	}
	// Dark paper-0 is 89% shadow and 11% paper over black.
	if !near(dark["--surface-page"], RGBA{0.11, 0.11, 0.11, 1}, 0.001) {
		t.Errorf("dark page: %v", dark["--surface-page"])
	}
	// --text-inverse differs by base: paper in light, forest in dark.
	if !near(light["--text-inverse"], RGBA{1, 1, 1, 1}, 0.001) || !near(dark["--text-inverse"], light["--brand-forest"], 0.001) {
		t.Errorf("text-inverse: %v / %v", light["--text-inverse"], dark["--text-inverse"])
	}
	// --ink-800 is 92% ink and 8% paper.
	want := RGBA{0x1f/255.0*0.92 + 0.08, 0x1b/255.0*0.92 + 0.08, 0x17/255.0*0.92 + 0.08, 1}
	if !near(light["--ink-800"], want, 0.001) {
		t.Errorf("ink-800: %v, want %v", light["--ink-800"], want)
	}
	// A translucent recipe keeps its alpha: --surface-hover is 30% sand over transparent.
	if got := light["--surface-hover"]; math.Abs(got.A-0.3) > 0.001 {
		t.Errorf("surface-hover alpha: %v", got)
	}
	for _, token := range []string{"--text-primary", "--text-secondary", "--surface-page", "--surface-card", "--surface-canvas", "--text-inverse", "--action-primary", "--status-critical"} {
		for base, m := range map[string]map[string]RGBA{"light": light, "dark": dark} {
			if _, ok := m[token]; !ok {
				t.Errorf("%s does not resolve for %s", token, base)
			}
		}
	}
}

func TestResolveOverrides(t *testing.T) {
	got, err := Resolve("light", map[string]string{"--color-paper": "#000000", "--color-ink": "#ffffff"})
	if err != nil {
		t.Fatal(err)
	}
	// Derived tokens follow the seeds.
	if !near(got["--surface-page"], RGBA{0, 0, 0, 1}, 0.001) || !near(got["--text-primary"], RGBA{1, 1, 1, 1}, 0.001) {
		t.Errorf("seeds did not flow: %v %v", got["--surface-page"], got["--text-primary"])
	}
	got, _ = Resolve("light", map[string]string{"--surface-page": "#102030"})
	if !near(got["--surface-page"], RGBA{0x10 / 255.0, 0x20 / 255.0, 0x30 / 255.0, 1}, 0.001) {
		t.Errorf("role override: %v", got["--surface-page"])
	}
	if _, err := Resolve("light", map[string]string{"--surface-page": "var(--x)"}); err == nil {
		t.Error("non-colour override accepted")
	}
	if _, err := Resolve("sepia", nil); err == nil {
		t.Error("unknown base accepted")
	}
}

func TestColorMix(t *testing.T) {
	e := &evaluator{decls: map[string]string{"--a": "#ff0000", "--b": "#0000ff"}, done: map[string]RGBA{}, busy: map[string]bool{}}
	for in, want := range map[string]RGBA{
		"color-mix(in srgb, var(--a) 25%, var(--b))":       {0.25, 0, 0.75, 1},
		"color-mix(in srgb, var(--a), var(--b) 25%)":       {0.75, 0, 0.25, 1},
		"color-mix(in srgb, var(--a), var(--b))":           {0.5, 0, 0.5, 1},
		"color-mix(in srgb, var(--a) 50%, transparent)":    {1, 0, 0, 0.5},
		"color-mix(in srgb, var(--a) 20%, var(--b) 20%)":   {0.5, 0, 0.5, 0.4},
		"color-mix(in srgb, var(--a) 150%, var(--b) 150%)": {0.5, 0, 0.5, 1},
	} {
		got, err := e.value(in)
		if err != nil || !near(got, want, 0.001) {
			t.Errorf("%s = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := e.value("color-mix(in oklab, var(--a), var(--b))"); err == nil {
		t.Error("oklab mix accepted")
	}
}

func TestContrast(t *testing.T) {
	for _, base := range []string{"light", "dark"} {
		for _, w := range Contrast(Theme{Name: "default", Base: base}) {
			t.Errorf("default %s palette: %s on %s is %.2f", base, w.Foreground, w.Background, w.Ratio)
		}
	}
	low := Theme{Name: "washed", Base: "light", Tokens: map[string]string{
		"--surface-page": "#ffffff", "--text-primary": "#cccccc", "--text-secondary": "#444444",
	}}
	got := Contrast(low)
	if len(got) != 1 || got[0].Foreground != "--text-primary" || got[0].Background != "--surface-page" || got[0].Ratio >= 1.7 {
		// --text-primary on card and canvas are low too.
		found := false
		for _, w := range got {
			if w.Foreground == "--text-primary" && w.Background == "--surface-page" {
				found = true
			}
		}
		if !found {
			t.Errorf("low contrast not flagged: %v", got)
		}
	}
	for _, w := range got {
		if w.Foreground == "--text-secondary" {
			t.Errorf("readable pair flagged: %v", w)
		}
	}
	// Translucent text is composited over the page: 10% black on white is unreadable.
	faint := Theme{Name: "faint", Base: "light", Tokens: map[string]string{"--text-primary": "rgba(0, 0, 0, 0.1)"}}
	if got := Contrast(faint); len(got) == 0 {
		t.Error("translucent text not flagged")
	}
	// A green action with white text below 4.5.
	pale := Theme{Name: "pale", Base: "light", Tokens: map[string]string{"--action-primary": "#9be08f"}}
	flagged := false
	for _, w := range Contrast(pale) {
		flagged = flagged || w.Background == "--action-primary"
	}
	if !flagged {
		t.Error("low contrast on the action colour not flagged")
	}
}
