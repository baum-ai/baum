package screens

import (
	"fmt"
	"html"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// SheetName is the file name of a pairing's contact sheet in the X run's
// pictures directory: sheet.html for a run's composition against its
// template, sheet-vs-<run>.html against another run.
func SheetName(p Pairing) string {
	if p.X.Run == p.Y.Run {
		return "sheet.html"
	}
	return "sheet-vs-" + safeName(p.Y.Run.Name()) + ".html"
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '-'
	}, s)
}

// WriteSheet writes a static contact sheet of a pairing to path: a summary of
// the first-screen facts of both sides, then per case both sides' first
// screens at 1440 and on a phone, linking to the full pages, beside their
// facts and, when verdicts are given, the judge's verdict. Pictures are
// linked by paths relative to the sheet, so it opens from the disk.
func WriteSheet(p Pairing, path string, verdicts map[string]PairVerdict) error {
	dir := filepath.Dir(path)
	rel := func(target string) string {
		r, err := filepath.Rel(dir, target)
		if err != nil {
			return target
		}
		return filepath.ToSlash(r)
	}
	ids := p.Cases()
	var b strings.Builder
	e := html.EscapeString
	fmt.Fprintf(&b, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>%s</title>
<style>
body{font:14px system-ui,sans-serif;margin:16px;background:#fafafa;color:#111}
table{border-collapse:collapse;margin:8px 0}td,th{border:1px solid #ddd;padding:3px 8px;text-align:left;font-variant-numeric:tabular-nums}
section{margin-top:28px;border-top:1px solid #ccc;padding-top:8px}
.pair{display:grid;grid-template-columns:1fr 1fr;gap:12px}
.side h3{margin:4px 0;font-size:14px}
.shots{display:flex;gap:8px;align-items:flex-start}
.shots img{border:1px solid #ccc;display:block}
.wide img{width:100%%;max-width:520px;height:auto}.phone img{height:300px;width:auto}
.facts{font-size:12px;color:#333}.facts li{margin:1px 0}
.verdict{font-weight:600}.issues{color:#a00}
nav a{margin-right:8px}
</style>
<h1>%s</h1>`, e(sheetTitle(p)), e(sheetTitle(p)))
	fmt.Fprintf(&b, `<p>%d cases. X is %s, Y is %s. First screens at 1440×900 unless marked phone (390×844).</p>`, len(ids), e(p.X.Label()), e(p.Y.Label()))
	b.WriteString(summaryTable(p, ids, verdicts))
	b.WriteString("<nav>")
	for i := range ids {
		fmt.Fprintf(&b, `<a href="#c%d">%d</a>`, i+1, i+1)
	}
	b.WriteString("</nav>")
	for i, id := range ids {
		c, _ := p.X.Run.Case(id)
		fmt.Fprintf(&b, `<section id="c%d"><h2>%d. %s <small>size %s · v%d</small></h2>`, i+1, i+1, e(id), e(c.Size), c.Version)
		if v, ok := verdicts[id]; ok {
			fmt.Fprintf(&b, `<p class="verdict">Judge: %s%s</p>`, e(verdictText(p, v)), e(reasonText(v)))
		}
		b.WriteString(`<div class="pair">`)
		for _, side := range []Side{p.X, p.Y} {
			wide := side.Run.Picture(id, side.Variant, 1440, false)
			wideFull := side.Run.Picture(id, side.Variant, 1440, true)
			phone := side.Run.Picture(id, side.Variant, 390, false)
			phoneFull := side.Run.Picture(id, side.Variant, 390, true)
			fmt.Fprintf(&b, `<div class="side"><h3>%s</h3><div class="shots"><a class="wide" href="%s"><img loading="lazy" src="%s" alt="first screen at 1440"></a><a class="phone" href="%s"><img loading="lazy" src="%s" alt="first screen on a phone"></a></div>`,
				e(side.Label()), e(rel(wideFull)), e(rel(wide)), e(rel(phoneFull)), e(rel(phone)))
			f, _ := side.Facts(id, false)
			phoneFacts, _ := side.Facts(id, true)
			b.WriteString(factsList(f, phoneFacts))
			b.WriteString(`</div>`)
		}
		b.WriteString(`</div>`)
		var issues []string
		for _, side := range []Side{p.X, p.Y} {
			sc, _ := side.Run.Case(id)
			for _, issue := range sc.Issues {
				if strings.HasPrefix(issue.Where, side.Variant) {
					issues = append(issues, issue.Where+": "+issue.What)
				}
			}
		}
		if len(issues) > 0 {
			b.WriteString(`<ul class="issues">`)
			for _, issue := range issues {
				fmt.Fprintf(&b, "<li>%s</li>", e(issue))
			}
			b.WriteString(`</ul>`)
		}
		b.WriteString(`</section>`)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func sheetTitle(p Pairing) string {
	if p.X.Run == p.Y.Run {
		return "Composed against template: " + p.X.Run.Name()
	}
	return "Composed: " + p.X.Run.Name() + " against " + p.Y.Run.Name()
}

func verdictText(p Pairing, v PairVerdict) string {
	switch v.Winner {
	case WinnerX:
		return p.X.Label() + " better"
	case WinnerY:
		return p.Y.Label() + " better"
	}
	if !v.OrdersAgree {
		return "tie (the two orders disagreed)"
	}
	return "tie"
}

func reasonText(v PairVerdict) string {
	if len(v.Reasons) == 0 {
		return ""
	}
	return " · " + strings.Join(v.Reasons, " | ")
}

// factsList lists a side's first-screen facts.
func factsList(f, phone FirstScreen) string {
	full, partial := 0, 0
	var kinds []string
	for _, t := range f.Tiles {
		if t.Visible == "full" {
			full++
		} else {
			partial++
		}
		kinds = append(kinds, t.Type+"/"+t.Size)
	}
	signal := func(given, visible bool) string {
		switch {
		case !given:
			return "–"
		case visible:
			return "shown"
		}
		return "not shown"
	}
	shown := func(list []Signal) string {
		if len(list) == 0 {
			return "–"
		}
		n := 0
		for _, s := range list {
			if s.Visible {
				n++
			}
		}
		return fmt.Sprintf("%d of %d", n, len(list))
	}
	var b strings.Builder
	b.WriteString(`<ul class="facts">`)
	fmt.Fprintf(&b, "<li>first screen: %d whole, %d cut · %s</li>", full, partial, html.EscapeString(strings.Join(kinds, ", ")))
	fmt.Fprintf(&b, "<li>landing tiles %d, %d below the first screen · wasted units %d (landing %d) · page %d px</li>", f.LandingTiles, f.BelowTiles, f.WastedUnits, f.WastedUnitsLanding, f.PageHeight)
	fmt.Fprintf(&b, "<li>recommendation %s · critical/high risks %s · named changes %s · breaking %s · missing evidence %s</li>",
		signal(f.Recommendation.Given, f.Recommendation.Visible), shown(f.SevereRisks), shown(f.NamedChanges), signal(f.Breaking.Given, f.Breaking.Visible),
		signal(f.MissingEvidence.Count > 0, f.MissingEvidence.Visible))
	fmt.Fprintf(&b, "<li>recall %s · phone recall %s · phone page %d px</li>", ratio(f.Recall), ratio(phone.Recall), phone.PageHeight)
	b.WriteString(`</ul>`)
	return b.String()
}

func ratio(r *float64) string {
	if r == nil {
		return "–"
	}
	return fmt.Sprintf("%.2f", *r)
}

// summaryTable sums both sides' first screens.
func summaryTable(p Pairing, ids []string, verdicts map[string]PairVerdict) string {
	var b strings.Builder
	b.WriteString(`<table><tr><th>first screen (1440)</th><th>X</th><th>Y</th></tr>`)
	sides := []Side{p.X, p.Y}
	screens := make([][]FirstScreen, 2)
	phones := make([][]FirstScreen, 2)
	for i, side := range sides {
		for _, id := range ids {
			if f, ok := side.Facts(id, false); ok {
				screens[i] = append(screens[i], f)
			}
			if f, ok := side.Facts(id, true); ok {
				phones[i] = append(phones[i], f)
			}
		}
	}
	recalls := []Recall{RecallOf(screens[0]), RecallOf(screens[1])}
	for k, kind := range recalls[0].Kinds() {
		other := recalls[1].Kinds()[k]
		fmt.Fprintf(&b, "<tr><td>recall: %s</td><td>%s</td><td>%s</td></tr>", html.EscapeString(kind.Name), countText(kind.Count), countText(other.Count))
	}
	phoneRecalls := []Recall{RecallOf(phones[0]), RecallOf(phones[1])}
	fmt.Fprintf(&b, "<tr><td>phone recall: all</td><td>%s</td><td>%s</td></tr>", countText(phoneRecalls[0].All), countText(phoneRecalls[1].All))
	for _, row := range []struct {
		name string
		of   func(FirstScreen) float64
	}{
		{"median whole tiles on the first screen", func(f FirstScreen) float64 { return float64(wholeTiles(f)) }},
		{"median landing tiles", func(f FirstScreen) float64 { return float64(f.LandingTiles) }},
		{"median wasted units, first screen", func(f FirstScreen) float64 { return float64(f.WastedUnits) }},
		{"median wasted units, landing", func(f FirstScreen) float64 { return float64(f.WastedUnitsLanding) }},
		{"median page height (px)", func(f FirstScreen) float64 { return float64(f.PageHeight) }},
	} {
		fmt.Fprintf(&b, "<tr><td>%s</td><td>%s</td><td>%s</td></tr>", row.name, median(screens[0], row.of), median(screens[1], row.of))
	}
	fmt.Fprintf(&b, "<tr><td>median phone page height (px)</td><td>%s</td><td>%s</td></tr>",
		median(phones[0], func(f FirstScreen) float64 { return float64(f.PageHeight) }), median(phones[1], func(f FirstScreen) float64 { return float64(f.PageHeight) }))
	if len(verdicts) > 0 {
		s := Summarize(verdicts)
		fmt.Fprintf(&b, "<tr><td>judge: X wins / ties / Y wins</td><td colspan=2>%d / %d / %d (sign test p = %.3g; orders agreed on %d of %d)</td></tr>",
			s.WinsX, s.Ties, s.WinsY, s.SignP, s.OrdersAgree, s.Pairs)
	}
	b.WriteString("</table>")
	return b.String()
}

func countText(c Count) string {
	if c.Given == 0 {
		return "–"
	}
	return fmt.Sprintf("%d of %d (%.0f%%)", c.Shown, c.Given, 100*c.Share())
}

func wholeTiles(f FirstScreen) int {
	n := 0
	for _, t := range f.Tiles {
		if t.Visible == "full" {
			n++
		}
	}
	return n
}

func median(screens []FirstScreen, of func(FirstScreen) float64) string {
	if len(screens) == 0 {
		return "–"
	}
	values := make([]float64, len(screens))
	for i, f := range screens {
		values[i] = of(f)
	}
	slices.Sort(values)
	m := values[len(values)/2]
	if len(values)%2 == 0 {
		m = (values[len(values)/2-1] + values[len(values)/2]) / 2
	}
	if m == math.Trunc(m) {
		return fmt.Sprintf("%.0f", m)
	}
	return fmt.Sprintf("%.1f", m)
}
