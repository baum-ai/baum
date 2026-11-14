package screens

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/baum-ai/baum/internal/replay"
	"github.com/baum-ai/baum/tiles"
)

// An owner labels pairs of layouts on a page (index.html) and records a
// verdict per pair in a plain text file (labels.txt), one line each:
//
//	<pair> <A|B|tie> [note]
//
// with blank lines and # comments ignored. A and B are the left and right
// layouts the page showed; key.json says which side of the pairing each was,
// so the verdicts can be compared with a judge's (AgreeLabels).

// KeySchemaName is the schema of a labelling set's key.
const KeySchemaName = "replay.labels-key/1"

// Key says which side each pair of a labelling set showed as A.
type Key struct {
	Schema string    `json:"schema"`
	X      string    `json:"x"`
	Y      string    `json:"y"`
	XRun   string    `json:"x_run"`
	YRun   string    `json:"y_run"`
	Seed   uint64    `json:"seed"`
	Pairs  []KeyPair `json:"pairs"`
}

// KeyPair is one pair of a labelling set: its name, its case and the side
// shown as A ("x" or "y").
type KeyPair struct {
	Pair string `json:"pair"`
	Case string `json:"case"`
	Size string `json:"size,omitempty"`
	Kind string `json:"kind,omitempty"`
	A    string `json:"a"`
}

// Label is an owner's verdict on a pair: A, B or tie.
type Label struct {
	Verdict string
	Note    string
}

// LabelSet makes a labelling set of n pairs into dir: index.html, which shows
// each pair's first screens (1440 and phone, embedded) with the full pages
// beside it (copied into dir), key.json and an empty labels.txt. Cases whose
// two layouts are the same are left out; the rest are spread over sizes and
// change kinds and each pair's sides are put in a random order, both from
// seed.
func LabelSet(p Pairing, n int, seed uint64, dir string) (Key, error) {
	key := Key{Schema: KeySchemaName, X: p.X.Label(), Y: p.Y.Label(), XRun: p.X.Run.Name(), YRun: p.Y.Run.Name(), Seed: seed}
	type candidate struct {
		id, size, kind string
	}
	var candidates []candidate
	for _, id := range p.Cases() {
		if p.X.Run == p.Y.Run && !differs(p.X.Run, id) {
			continue
		}
		c, _ := p.X.Run.Case(id)
		candidates = append(candidates, candidate{id: id, size: c.Size, kind: changeKind(p.X.Run.Dir, id)})
	}
	if len(candidates) < n {
		return key, fmt.Errorf("only %d cases have two different layouts; %d were asked for", len(candidates), n)
	}
	random := rand.New(rand.NewPCG(seed, 0x6c6162656c))
	random.Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })
	// Round robin over the sizes, each time the candidate of the least
	// represented change kind.
	var sizes []string
	for _, c := range candidates {
		if !slices.Contains(sizes, c.size) {
			sizes = append(sizes, c.size)
		}
	}
	slices.SortFunc(sizes, func(a, b string) int { return sizeRank(a) - sizeRank(b) })
	kinds := map[string]int{}
	taken := map[int]bool{}
	var chosen []candidate
	for len(chosen) < n {
		for _, size := range sizes {
			best := -1
			for i, c := range candidates {
				if taken[i] || c.size != size {
					continue
				}
				if best < 0 || kinds[c.kind] < kinds[candidates[best].kind] {
					best = i
				}
			}
			if best >= 0 && len(chosen) < n {
				taken[best] = true
				kinds[candidates[best].kind]++
				chosen = append(chosen, candidates[best])
			}
		}
	}
	random.Shuffle(len(chosen), func(i, j int) { chosen[i], chosen[j] = chosen[j], chosen[i] })
	if err := os.MkdirAll(filepath.Join(dir, "full"), 0o700); err != nil {
		return key, err
	}
	var page strings.Builder
	writePageHead(&page, n)
	for i, c := range chosen {
		pair := KeyPair{Pair: fmt.Sprintf("P%02d", i+1), Case: c.id, Size: c.size, Kind: c.kind, A: WinnerX}
		a, b := p.X, p.Y
		if random.IntN(2) == 1 {
			pair.A, a, b = WinnerY, p.Y, p.X
		}
		key.Pairs = append(key.Pairs, pair)
		facts, err := reviewFacts(p.X.Run.Dir, c.id)
		if err != nil {
			return key, err
		}
		if err := writePagePair(&page, dir, pair, a, b, facts); err != nil {
			return key, err
		}
	}
	page.WriteString(pageScript)
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(page.String()), 0o600); err != nil {
		return key, err
	}
	raw, err := json.MarshalIndent(key, "", "  ")
	if err != nil {
		return key, err
	}
	if err := os.WriteFile(filepath.Join(dir, "key.json"), raw, 0o600); err != nil {
		return key, err
	}
	var labels strings.Builder
	labels.WriteString("# Owner labels: one pair per line, <pair> <A|B|tie> [note]. A is the left layout.\n")
	for _, pair := range key.Pairs {
		labels.WriteString(pair.Pair + " \n")
	}
	labelsPath := filepath.Join(dir, "labels.txt")
	if _, err := os.Stat(labelsPath); err == nil {
		return key, nil // the owner's labels are never overwritten
	}
	return key, os.WriteFile(labelsPath, []byte(labels.String()), 0o600)
}

func sizeRank(size string) int {
	return slices.Index([]string{"xs", "s", "m", "l", "xl"}, size)
}

// differs reports whether a run's composition of a case differs from its
// template, as its compose record says.
func differs(run *Run, id string) bool {
	for _, name := range []string{"compose.json", "document.json"} {
		raw, err := os.ReadFile(filepath.Join(run.Dir, "cases", id, name))
		if err != nil {
			continue
		}
		var record replay.ComposeRecord
		if json.Unmarshal(raw, &record) == nil && record.Schema == replay.ComposeRecordName {
			return record.DiffersFromTemplate
		}
	}
	return true
}

// changeKind is the overview's change kind of a case's review.
func changeKind(runDir, id string) string {
	raw, err := os.ReadFile(filepath.Join(runDir, "cases", id, "tiles.json"))
	if err != nil {
		return ""
	}
	var doc tiles.Document
	if json.Unmarshal(raw, &doc) != nil {
		return ""
	}
	for _, t := range doc.Tiles {
		if t.Type == tiles.TypeOverview {
			var data tiles.OverviewData
			_ = json.Unmarshal(t.Data, &data)
			return data.ChangeKind
		}
	}
	return ""
}

func writePageHead(b *strings.Builder, n int) {
	fmt.Fprintf(b, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Layout labels</title>
<style>
:root{color-scheme:light;--bg:#fafafa;--fg:#111;--muted:#555;--line:#ccc;--pick:#2563eb}
body{font:15px system-ui,sans-serif;margin:16px;background:var(--bg);color:var(--fg)}
header{position:sticky;top:0;background:var(--bg);padding:8px 0;border-bottom:1px solid var(--line);z-index:1;display:flex;gap:12px;align-items:center;flex-wrap:wrap}
section{margin:24px 0;border-bottom:1px solid var(--line);padding-bottom:16px}
.pair{display:grid;grid-template-columns:1fr 1fr;gap:16px}
.side h3{margin:4px 0}
.side img{border:1px solid var(--line);display:block}
.wide{width:100%%;height:auto}.phone{height:360px;width:auto;margin-top:8px}
.vote{display:flex;gap:8px;align-items:center;margin-top:8px;flex-wrap:wrap}
.vote button{font:inherit;padding:6px 16px;border:1px solid var(--line);border-radius:6px;background:#fff;cursor:pointer}
.vote button[aria-pressed=true]{background:var(--pick);color:#fff;border-color:var(--pick)}
.vote input{flex:1;min-width:200px;font:inherit;padding:5px}
details{margin-top:8px;color:var(--muted);white-space:pre-wrap;font-size:13px}
textarea{width:100%%;height:160px;font:13px ui-monospace,monospace}
@media (max-width:700px){.pair{grid-template-columns:1fr}}
</style>
<h1>Which layout is better?</h1>
<p>%d pairs. Each shows the same review twice, laid out two ways: A on the left, B on the right, the first screen at 1440 wide and on a phone. Click a picture for the whole page. Pick A, B or tie for each pair (keys: a, b, t; j and k move between pairs), then save the labels and put them in labels.txt beside this page. Judge what a reviewer sees first and whether anything important is buried, not the writing, which is the same in both.</p>
<header><span id="count"></span><button id="save" type="button">Download labels.txt</button><button id="copy" type="button">Copy labels</button></header>
`, n)
}

func writePagePair(b *strings.Builder, dir string, pair KeyPair, a, bSide Side, facts string) error {
	e := html.EscapeString
	fmt.Fprintf(b, `<section id="%s" data-pair="%s"><h2>%s <small>(size %s)</small></h2><div class="pair">`, e(pair.Pair), e(pair.Pair), e(pair.Pair), e(pair.Size))
	for i, side := range []Side{a, bSide} {
		letter := []string{"A", "B"}[i]
		wide, err := dataURI(side.Run.Picture(pair.Case, side.Variant, 1440, false))
		if err != nil {
			return err
		}
		phone, err := dataURI(side.Run.Picture(pair.Case, side.Variant, 390, false))
		if err != nil {
			return err
		}
		var links []string
		for _, width := range []int{1440, 390} {
			name := fmt.Sprintf("%s-%s-%d.png", pair.Pair, letter, width)
			if err := copyFile(side.Run.Picture(pair.Case, side.Variant, width, true), filepath.Join(dir, "full", name)); err != nil {
				return err
			}
			links = append(links, fmt.Sprintf(`<a href="full/%s">whole page at %d</a>`, name, width))
		}
		fmt.Fprintf(b, `<div class="side"><h3>%s</h3><a href="full/%s-%s-1440.png"><img class="wide" src="%s" alt="%s at 1440"></a><a href="full/%s-%s-390.png"><img class="phone" src="%s" alt="%s on a phone"></a><p>%s</p></div>`,
			letter, e(pair.Pair), letter, wide, letter, e(pair.Pair), letter, phone, letter, strings.Join(links, " · "))
	}
	fmt.Fprintf(b, `</div><div class="vote"><button type="button" data-v="A" aria-pressed="false">A</button><button type="button" data-v="B" aria-pressed="false">B</button><button type="button" data-v="tie" aria-pressed="false">tie</button><input type="text" placeholder="note (optional)" aria-label="note"></div><details><summary>The review's facts</summary>%s</details></section>
`, e(facts))
	return nil
}

func dataURI(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw), nil
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// pageScript keeps the choices in the browser's storage and writes them in
// the label file's format.
const pageScript = `<h2>Labels</h2><textarea id="out" readonly></textarea>
<script>
const KEY = "replay-labels:" + location.pathname;
const sections = [...document.querySelectorAll("section[data-pair]")];
let saved = {};
try { saved = JSON.parse(localStorage.getItem(KEY) || "{}"); } catch {}
function store() { try { localStorage.setItem(KEY, JSON.stringify(saved)); } catch {} }
function text() {
  return "# Owner labels: one pair per line, <pair> <A|B|tie> [note]. A is the left layout.\n" +
    sections.map((s) => { const v = saved[s.dataset.pair] || {}; return (s.dataset.pair + " " + (v.verdict || "") + (v.note ? " " + v.note.replace(/\s+/g, " ") : "")).trimEnd(); }).join("\n") + "\n";
}
function render() {
  let done = 0;
  for (const s of sections) {
    const v = saved[s.dataset.pair] || {};
    if (v.verdict) done++;
    for (const b of s.querySelectorAll("button[data-v]")) b.setAttribute("aria-pressed", String(b.dataset.v === v.verdict));
    const note = s.querySelector("input"); if (document.activeElement !== note) note.value = v.note || "";
  }
  document.getElementById("count").textContent = done + " of " + sections.length + " labelled";
  document.getElementById("out").value = text();
}
for (const s of sections) {
  for (const b of s.querySelectorAll("button[data-v]")) b.addEventListener("click", () => { saved[s.dataset.pair] = { ...(saved[s.dataset.pair] || {}), verdict: b.dataset.v }; store(); render(); });
  s.querySelector("input").addEventListener("input", (e) => { saved[s.dataset.pair] = { ...(saved[s.dataset.pair] || {}), note: e.target.value }; store(); render(); });
}
function current() { const y = innerHeight / 3; return sections.findIndex((s) => s.getBoundingClientRect().bottom > y); }
document.addEventListener("keydown", (e) => {
  if (e.target instanceof HTMLInputElement) return;
  const i = Math.max(current(), 0);
  const choice = { a: "A", b: "B", t: "tie" }[e.key];
  if (choice) { const p = sections[i].dataset.pair; saved[p] = { ...(saved[p] || {}), verdict: choice }; store(); render(); sections[Math.min(i + 1, sections.length - 1)].scrollIntoView(); }
  if (e.key === "j") sections[Math.min(i + 1, sections.length - 1)].scrollIntoView();
  if (e.key === "k") sections[Math.max(i - 1, 0)].scrollIntoView();
});
document.getElementById("save").addEventListener("click", () => {
  const a = document.createElement("a");
  a.href = URL.createObjectURL(new Blob([text()], { type: "text/plain" }));
  a.download = "labels.txt"; a.click();
});
document.getElementById("copy").addEventListener("click", () => navigator.clipboard?.writeText(text()));
render();
</script>
`

// ReadLabels reads a label file. A line without a verdict is a pair not yet
// labelled and is left out; any other verdict than A, B or tie is an error.
func ReadLabels(path string) (map[string]Label, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	labels := map[string]Label{}
	scanner := bufio.NewScanner(f)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Fields(text)
		if len(fields) < 2 {
			continue
		}
		verdict := fields[1]
		switch strings.ToLower(verdict) {
		case "a":
			verdict = "A"
		case "b":
			verdict = "B"
		case "tie":
			verdict = "tie"
		default:
			return nil, fmt.Errorf("%s:%d: the verdict %q is not A, B or tie", path, line, verdict)
		}
		labels[fields[0]] = Label{Verdict: verdict, Note: strings.Join(fields[2:], " ")}
	}
	return labels, scanner.Err()
}

// ReadKey reads a labelling set's key.json.
func ReadKey(path string) (Key, error) {
	var key Key
	raw, err := os.ReadFile(path)
	if err != nil {
		return key, err
	}
	if err := json.Unmarshal(raw, &key); err != nil || key.Schema != KeySchemaName {
		return key, errors.New(path + " is not a labelling key")
	}
	return key, nil
}

// LabelAgreement compares an owner's labels with a judge's pair verdicts.
type LabelAgreement struct {
	// Labelled is the pairs the owner labelled that the judge also decided.
	Labelled int `json:"labelled"`
	// Same is how many verdicts match (a tie matching a tie).
	Same int `json:"same"`
	// Decisive counts pairs both call decisive, and DecisiveSame those that
	// agree.
	Decisive     int `json:"decisive"`
	DecisiveSame int `json:"decisive_same"`
	// Pairs lists each pair: the owner's and the judge's winner (x, y, tie).
	Pairs []LabelPair `json:"pairs"`
}

// LabelPair is one labelled pair, in the pairing's terms.
type LabelPair struct {
	Pair  string `json:"pair"`
	Case  string `json:"case"`
	Owner string `json:"owner"`
	Judge string `json:"judge"`
}

// AgreeLabels compares the owner's labels with a judge report's verdicts,
// translating A and B through the key.
func AgreeLabels(key Key, labels map[string]Label, report Report) LabelAgreement {
	var out LabelAgreement
	for _, pair := range key.Pairs {
		label, ok := labels[pair.Pair]
		if !ok {
			continue
		}
		verdict, ok := report.Pairs[pair.Case]
		if !ok {
			continue
		}
		owner := WinnerTie
		switch label.Verdict {
		case "A":
			owner = pair.A
		case "B":
			owner = WinnerX
			if pair.A == WinnerX {
				owner = WinnerY
			}
		}
		out.Labelled++
		if owner == verdict.Winner {
			out.Same++
		}
		if owner != WinnerTie && verdict.Winner != WinnerTie {
			out.Decisive++
			if owner == verdict.Winner {
				out.DecisiveSame++
			}
		}
		out.Pairs = append(out.Pairs, LabelPair{Pair: pair.Pair, Case: pair.Case, Owner: owner, Judge: verdict.Winner})
	}
	return out
}
