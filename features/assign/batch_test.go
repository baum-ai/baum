package assign

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestBatchPromptAppliesTheSingleItemRules(t *testing.T) {
	batch, ok := Public.BatchVersion(PublicVersion)
	if !ok || batch != PublicBatchVersion {
		t.Fatalf("BatchVersion(%s) = %q, %v", PublicVersion, batch, ok)
	}
	prompt, single := Public.batch[batch], Public.single[PublicVersion].Prompt
	start, end := strings.Index(single, rulesStart), strings.Index(single, rulesEnd)
	rules := strings.TrimSpace(single[start:end])
	definition := single[len(singleOpening):strings.Index(single, replyLine)]
	for _, part := range []string{rules, definition} {
		if !strings.Contains(prompt, part) {
			t.Fatalf("%s lacks part of the single-item prompt:\n%s", batch, part)
		}
	}
	for _, left := range []string{"{{", "}}", singleOpening, rulesEnd, `"item": the work to assign`} {
		if strings.Contains(prompt, left) {
			t.Fatalf("%s still contains %q", batch, left)
		}
	}
	if Public.KnownPromptVersion(batch) || !Public.KnownBatchPromptVersion(batch) || Public.KnownBatchPromptVersion(PublicVersion) {
		t.Fatal("a batch version must be known as a batch version only")
	}
	// The batch fields name an outcome, as the single-item prompt does, not a
	// broad area.
	if strings.Contains(prompt, "broad capability or engineering area") || !strings.Contains(prompt, "Name the outcome the item works towards") {
		t.Fatalf("%s describes a new feature by its area", batch)
	}
	if _, ok := testPrompts.BatchVersion(plainVersion); ok {
		t.Fatal("a version without a batch version has one")
	}
}

// A seeded batch version is its batch prompt with one note on candidates
// with no pull requests; the batch version itself is unchanged.
func TestSeededBatchPromptAddsOnlyItsNote(t *testing.T) {
	batch := PublicBatchVersion
	seeded, note := Public.SeededBatch(batch), publicSeededNote
	if seeded == batch || !Public.KnownBatchPromptVersion(seeded) || Public.KnownPromptVersion(seeded) || Public.SeededBatch(seeded) != seeded {
		t.Fatalf("SeededBatch(%s) = %q", batch, seeded)
	}
	prompt := Public.batch[batch]
	if strings.Contains(prompt, note) || Public.batch[seeded] != strings.Replace(prompt, CandidatesEnd, CandidatesEnd+note, 1) {
		t.Fatalf("%s is not %s with its note:\n%s", seeded, batch, Public.batch[seeded])
	}
	req, err := Public.Request(seeded, "{}", 0)
	if err != nil {
		t.Fatal(err)
	}
	if version, ok := Public.VersionOf(req); !ok || version != seeded {
		t.Fatalf("VersionOf = %q, %v", version, ok)
	}
}

// The seeded note judges a proposed feature by its outcome: its paths,
// which can be broad, do not widen it.
func TestSeededNoteDoesNotWidenAProposedFeatureByItsPaths(t *testing.T) {
	prompt := Public.batch[Public.SeededBatch(PublicBatchVersion)]
	if !strings.Contains(prompt, "never widen that outcome") || strings.Contains(prompt, "judge it by its description and paths") {
		t.Fatal("the seeded batch version judges a proposed feature by its paths")
	}
}

func TestComposeBatchPromptRefusesAPromptWithoutRules(t *testing.T) {
	for _, prompt := range []string{"", singleOpening + "x.", singleOpening + "Def." + replyLine + "\n\nno rules"} {
		if _, err := composeBatchPrompt(prompt, publicBatchFrame); err == nil {
			t.Fatalf("composed a batch prompt from %q", prompt)
		}
	}
}

func TestRequestAndVersionOfBatch(t *testing.T) {
	req, err := Public.Request(PublicBatchVersion, "{}", 0)
	if err != nil {
		t.Fatal(err)
	}
	if req.SchemaName != BatchSchemaName || req.MaxOutputTokens != MaxBatchOutputTokens {
		t.Fatalf("request: %s, %d", req.SchemaName, req.MaxOutputTokens)
	}
	if version, ok := Public.VersionOf(req); !ok || version != PublicBatchVersion {
		t.Fatalf("VersionOf = %q, %v", version, ok)
	}
	single, err := Public.Request(PublicVersion, "{}", 0)
	if err != nil {
		t.Fatal(err)
	}
	if version, ok := Public.VersionOf(single); !ok || version != PublicVersion {
		t.Fatalf("VersionOf(single) = %q, %v", version, ok)
	}
	// A batch prompt under the single schema name is no known request.
	single.System = req.System
	if _, ok := Public.VersionOf(single); ok {
		t.Fatal("a mismatched schema name and prompt resolved to a version")
	}
	if limits, ok := Public.LimitsOf(PublicBatchVersion); !ok || limits.MaxInputBytes != MaxBatchInputBytes || limits.MaxOutputTokens != MaxBatchOutputTokens {
		t.Fatalf("batch limits %+v, %v", limits, ok)
	}
	if limits, ok := Public.LimitsOf(PublicVersion); !ok || limits.MaxInputBytes != MaxInputBytes(MaxCandidates) || limits.MaxOutputTokens != MaxOutputTokens {
		t.Fatalf("single limits %+v, %v", limits, ok)
	}
	if _, ok := Public.LimitsOf("feature-assign-v9"); ok {
		t.Fatal("limits of an unknown version")
	}
	if _, err := json.Marshal(BatchSchema()); err != nil {
		t.Fatal(err)
	}
}

func TestRenderBatch(t *testing.T) {
	items := []BatchItem{
		{PullRequest: PullRequest{Title: "Add CSV export", Body: "See #12", Branch: "csv", Paths: []string{"export/csv.go"}}, Number: 11,
			Links: map[string]Links{"uuid-2": {Stacked: []int32{7}}}, Mentions: []int{1, 1, 0, 5}},
		{PullRequest: PullRequest{Title: "Fix export", Paths: []string{"export/fix.go"}}, Number: 12},
	}
	candidates := []Candidate{
		{ID: "uuid-1", Title: "Parser", Current: true, Paths: []string{"a.go", "export/fix.go"}, Digest: "parses", PullRequests: []CandidatePullRequest{{Title: "p", Summary: "s"}},
			Links: Links{Mentioned: []int32{99}}},
		{ID: "uuid-2", Title: "Hidden", Withheld: true, Digest: "secret", PullRequests: []CandidatePullRequest{{Title: "q", Summary: "secret"}}},
	}
	rendered, err := Public.RenderBatch(PublicBatchVersion, items, candidates)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Items []struct {
			ID            string
			Number        int32
			MentionsItems []string `json:"mentions_items"`
			Links         []struct {
				Feature string
				Stacked []int32
			}
		}
		Candidates []struct {
			ID, Title string
			Paths     []string
			Links     any
		}
	}
	if err := json.Unmarshal([]byte(rendered), &decoded); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rendered, "uuid-") || strings.Contains(rendered, "secret") {
		t.Fatalf("an id or withheld text leaked: %s", rendered)
	}
	first, second := decoded.Items[0], decoded.Items[1]
	if first.ID != "P1" || first.Number != 11 || len(first.MentionsItems) != 1 || first.MentionsItems[0] != "P2" ||
		len(first.Links) != 1 || first.Links[0].Feature != "F2" || first.Links[0].Stacked[0] != 7 {
		t.Fatalf("first item %+v", first)
	}
	if second.ID != "P2" {
		t.Fatalf("second item %+v", second)
	}
	if decoded.Candidates[0].Paths[0] != "export/fix.go" || decoded.Candidates[0].Links != nil || decoded.Candidates[1].Title != withheldText ||
		strings.Contains(rendered, `"current":true`) {
		t.Fatalf("candidates %+v", decoded.Candidates)
	}

	for name, call := range map[string]func() (string, error){
		"unknown version": func() (string, error) { return Public.RenderBatch(PublicVersion, items, nil) },
		"no items":        func() (string, error) { return Public.RenderBatch(PublicBatchVersion, nil, nil) },
		"too many items": func() (string, error) {
			return Public.RenderBatch(PublicBatchVersion, make([]BatchItem, MaxBatchItems+1), nil)
		},
		"too many candidates": func() (string, error) {
			return Public.RenderBatch(PublicBatchVersion, items, make([]Candidate, MaxBatchCandidates+1))
		},
	} {
		if _, err := call(); err == nil {
			t.Errorf("%s: rendered", name)
		}
	}
}

func TestRenderBatchIsBounded(t *testing.T) {
	huge := strings.Repeat("<long & \"text\"> ", 10000)
	var paths []string
	for i := range 200 {
		paths = append(paths, fmt.Sprintf("dir/%d/%s.go", i, strings.Repeat("p", 500)))
	}
	batch := func(items, candidates int) ([]BatchItem, []Candidate) {
		batchItems := make([]BatchItem, items)
		for i := range batchItems {
			batchItems[i] = BatchItem{PullRequest: PullRequest{Title: huge, Body: huge, Branch: huge, Paths: paths}}
		}
		offered := make([]Candidate, candidates)
		for i := range offered {
			pulls := make([]CandidatePullRequest, 50)
			for j := range pulls {
				pulls[j] = CandidatePullRequest{Title: huge, Summary: huge}
			}
			offered[i] = Candidate{ID: fmt.Sprint(i), Title: huge, Description: huge, Paths: paths, Digest: huge, PullRequests: pulls}
		}
		return batchItems, offered
	}
	// A few items at every bound fit.
	items, candidates := batch(2, 2)
	rendered, err := Public.RenderBatch(PublicBatchVersion, items, candidates)
	if err != nil || len(rendered) > MaxBatchInputBytes {
		t.Fatalf("small batch: %d bytes, %v", len(rendered), err)
	}
	// A full batch at every bound does not, and is refused for the caller to
	// split.
	items, candidates = batch(MaxBatchItems, MaxBatchCandidates)
	if _, err := Public.RenderBatch(PublicBatchVersion, items, candidates); !errors.Is(err, ErrBatchTooLarge) {
		t.Fatalf("full batch: %v, want ErrBatchTooLarge", err)
	}
}

func batchJSON(assignments ...string) []byte {
	return []byte(`{"assignments":[` + strings.Join(assignments, ",") + `]}`)
}

func assignment(item, choice, featureID, newID, title, description string) string {
	encoded, _ := json.Marshal(map[string]string{
		"item": item, "reason": "r", "choice": choice, "feature_id": featureID, "new_id": newID, "title": title, "description": description,
	})
	return string(encoded)
}

func TestParseBatchReply(t *testing.T) {
	candidates := []Candidate{{ID: "uuid-1"}, {ID: "uuid-2"}}
	reply, err := ParseBatchReply(batchJSON(
		assignment("P1", "new", "", "N1", "  CSV   export ", "Exports."),
		assignment("P3", "existing", "N1", "", "", ""),
		assignment("P2", "existing", "F2", "", "", ""),
		assignment("P4", "skip", "", "", "", ""),
		assignment("P5", "new", "", "N2", "Billing", "Bills."),
	), 5, candidates)
	if err != nil {
		t.Fatal(err)
	}
	want := []BatchChoice{{New: 0, Reason: "r"}, {FeatureID: "uuid-2", New: -1, Reason: "r"}, {New: 0, Reason: "r"}, {New: -1, Skip: true, Reason: "r"}, {New: 1, Reason: "r"}}
	for i, choice := range reply.Choices {
		if choice != want[i] {
			t.Fatalf("choice %d = %+v, want %+v", i, choice, want[i])
		}
	}
	if len(reply.New) != 2 || reply.New[0].Title != "CSV export" || reply.New[1].Title != "Billing" {
		t.Fatalf("new features %+v", reply.New)
	}

	for name, data := range map[string][]byte{
		"not JSON":          []byte("nope"),
		"trailing":          append(batchJSON(assignment("P1", "skip", "", "", "", "")), []byte(" {}")...),
		"no assignments":    []byte(`{}`),
		"unknown field":     []byte(`{"assignments":[],"x":1}`),
		"missing field":     batchJSON(`{"item":"P1","reason":"r","choice":"skip","feature_id":"","title":"","description":""}`),
		"item unanswered":   batchJSON(assignment("P1", "skip", "", "", "", "")),
		"item twice":        batchJSON(assignment("P1", "skip", "", "", "", ""), assignment("P1", "skip", "", "", "", ""), assignment("P2", "skip", "", "", "", "")),
		"unknown item":      batchJSON(assignment("P1", "skip", "", "", "", ""), assignment("P2", "skip", "", "", "", ""), assignment("P3", "skip", "", "", "", "")),
		"not a candidate":   batchJSON(assignment("P1", "existing", "F3", "", "", ""), assignment("P2", "skip", "", "", "", "")),
		"undefined new":     batchJSON(assignment("P1", "existing", "N1", "", "", ""), assignment("P2", "skip", "", "", "", "")),
		"joins a later new": batchJSON(assignment("P1", "existing", "N1", "", "", ""), assignment("P2", "new", "", "N1", "T", "D")),
		"joins its own new": batchJSON(assignment("P1", "new", "", "N1", "T", "D"), assignment("P1", "existing", "N1", "", "", "")),
		"new id twice":      batchJSON(assignment("P1", "new", "", "N1", "T", "D"), assignment("P2", "new", "", "N1", "T", "D")),
		"bad new id":        batchJSON(assignment("P1", "new", "", "F1", "T", "D"), assignment("P2", "skip", "", "", "", "")),
		"leading zero":      batchJSON(assignment("P1", "new", "", "N01", "T", "D"), assignment("P2", "skip", "", "", "", "")),
		"new without text":  batchJSON(assignment("P1", "new", "", "N1", "T", " "), assignment("P2", "skip", "", "", "", "")),
		"title too long":    batchJSON(assignment("P1", "new", "", "N1", strings.Repeat("t", MaxNewTitleRunes+1), "D"), assignment("P2", "skip", "", "", "", "")),
		"unknown choice":    batchJSON(assignment("P1", "maybe", "", "", "", ""), assignment("P2", "skip", "", "", "", "")),
	} {
		if _, err := ParseBatchReply(data, 2, candidates); !errors.Is(err, ErrInvalidReply) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := ParseBatchReply(batchJSON(), 0, candidates); err == nil {
		t.Error("parsed a reply for no items")
	}
}

// A candidate lists first the paths an item shows, not those cut from it.
func TestRenderBatchListsTheVisibleOverlapFirst(t *testing.T) {
	var itemPaths, candidatePaths []string
	for i := range 75 {
		itemPaths = append(itemPaths, fmt.Sprintf("p%02d.go", i))
	}
	// Many matches among the item's hidden paths, then one it shows.
	candidatePaths = append(candidatePaths, itemPaths[maxBatchItemPaths:]...)
	candidatePaths = append(candidatePaths, itemPaths[0])
	rendered, err := Public.RenderBatch(PublicBatchVersion,
		[]BatchItem{{PullRequest: PullRequest{Title: "Wide", Paths: itemPaths}}}, []Candidate{{ID: "uuid-1", Paths: candidatePaths}})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Candidates []struct{ Paths []string }
	}
	if err := json.Unmarshal([]byte(rendered), &decoded); err != nil {
		t.Fatal(err)
	}
	if paths := decoded.Candidates[0].Paths; len(paths) == 0 || paths[0] != "p00.go" {
		t.Fatalf("candidate paths %v, want the overlap the item shows first", paths)
	}
}
