package review

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// SectionTitle is the label of the section at a 0-based position. Validation
// rejects blank titles, but documents stored before that keep them; such a
// section is labelled by its reading order, which is fixed once published.
func SectionTitle(title string, position int) string {
	if strings.TrimSpace(title) == "" {
		return "Section " + strconv.Itoa(position+1)
	}
	return title
}

// labelBlankSectionTitles applies SectionTitle to a stored document, so it
// still validates when it is refreshed or carried into the next version.
func labelBlankSectionTitles(document map[string]any) {
	sections, _ := document["sections"].([]any)
	for i, raw := range sections {
		if section, ok := raw.(map[string]any); ok {
			title, _ := section["title"].(string)
			section["title"] = SectionTitle(title, i)
		}
	}
}

// These types model the document fields used by generation and refresh.
// Polymorphic content that passes through unchanged remains raw JSON; the
// pinned JSON schema validates it before and after document updates.

// reviewDocument is a stored review document as the generation and refresh code
// reads it.
type reviewDocument struct {
	Schema          string                `json:"schema"`
	Review          documentReview        `json:"review"`
	Summary         documentSummary       `json:"summary"`
	Stats           documentStats         `json:"stats"`
	Sections        []documentSection     `json:"sections"`
	AgentReviews    []documentAgentReview `json:"agent_reviews"`
	UnassignedHunks []string              `json:"unassigned_hunks"`
	Index           documentIndex         `json:"index"`
}

type documentReview struct {
	Repository          string                  `json:"repository"`
	PullRequest         int                     `json:"pull_request"`
	PRState             string                  `json:"pr_state"`
	Head                string                  `json:"head"`
	Base                string                  `json:"base"`
	MergeBase           string                  `json:"merge_base"`
	MergedCommit        *string                 `json:"merged_commit"`
	Version             int                     `json:"version"`
	PreviousVersion     *int                    `json:"previous_version"`
	GenerationState     string                  `json:"generation_state"`
	GeneratedAt         string                  `json:"generated_at"`
	EvidenceRefreshedAt string                  `json:"evidence_refreshed_at"`
	Generator           documentReviewGenerator `json:"generator"`
	Sources             documentReviewSources   `json:"sources"`
	Relations           json.RawMessage         `json:"relations"`
}

type documentReviewGenerator struct {
	Model    string  `json:"model"`
	ReviewMD *string `json:"review_md"`
	// ReviewMDChanged marks a review whose pull request changes REVIEW.md;
	// the base version was applied and the page says so.
	ReviewMDChanged bool     `json:"review_md_changed"`
	Truncated       []string `json:"truncated"`
	SchemaRevision  string   `json:"schema_revision"`
}

type documentReviewSources struct {
	Diff          bool `json:"diff"`
	Sessions      int  `json:"sessions"`
	Checks        int  `json:"checks"`
	TestReports   int  `json:"test_reports"`
	ReviewThreads bool `json:"review_threads"`
	AgentReviews  int  `json:"agent_reviews"`
	SpecLinks     bool `json:"spec_links"`
	ReviewMD      bool `json:"review_md"`
}

type documentSummary struct {
	Title          string                 `json:"title"`
	Text           string                 `json:"text"`
	ReviewExchange documentReviewExchange `json:"review_exchange"`
	ChangeKind     string                 `json:"change_kind"`
	Implements     json.RawMessage        `json:"implements"`
	ReleaseNote    *string                `json:"release_note"`
	BreakingChange *string                `json:"breaking_change"`
	Attention      json.RawMessage        `json:"attention"`
	Author         documentAuthor         `json:"author"`
	Authorship     documentAuthorship     `json:"authorship"`
	// RiskScore is absent on documents generated before assessments existed.
	RiskScore *documentRiskScore `json:"risk_score,omitempty"`
}

type documentRiskScore struct {
	Rubric      string                    `json:"rubric"`
	Status      string                    `json:"status"`
	Score       *int                      `json:"score"`
	Reasons     []documentRiskScoreReason `json:"reasons"`
	Limitations []documentRiskLimitation  `json:"limitations"`
}

type documentRiskScoreReason struct {
	Section string  `json:"section"`
	Risk    *string `json:"risk"`
	Text    string  `json:"text"`
}

type documentRiskLimitation struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

type documentReviewExchange struct {
	Summary             string                    `json:"summary"`
	Reviews             int                       `json:"reviews"`
	BackAndForths       *int                      `json:"back_and_forths"`
	CriticalIssuesFound *int                      `json:"critical_issues_found"`
	ChangesAfterOpen    *documentChangesAfterOpen `json:"changes_after_open"`
}

// documentChangesAfterOpen counts what the pull request gained after it was opened.
type documentChangesAfterOpen struct {
	Commits      int `json:"commits"`
	LinesAdded   int `json:"lines_added"`
	LinesRemoved int `json:"lines_removed"`
}

type documentAuthor struct {
	Login       string `json:"login"`
	Association string `json:"association"`
	Bot         bool   `json:"bot"`
}

type documentAuthorship struct {
	Declared         string           `json:"declared"`
	DeclaredTools    []string         `json:"declared_tools"`
	Anchors          []documentAnchor `json:"anchors"`
	ObservedSessions int              `json:"observed_sessions"`
	ObservedVendors  []string         `json:"observed_vendors"`
	ObservedModels   []string         `json:"observed_models,omitzero"`
}

type documentStats struct {
	Sections      int                        `json:"sections"`
	Files         int                        `json:"files"`
	Hunks         int                        `json:"hunks"`
	Additions     int                        `json:"additions"`
	Deletions     int                        `json:"deletions"`
	RiskLOC       map[string]int             `json:"risk_loc"`
	RoleLOC       map[string]int             `json:"role_loc"`
	Checks        documentStatsChecks        `json:"checks"`
	AgentFindings documentStatsAgentFindings `json:"agent_findings"`
}

type documentStatsChecks struct {
	RequiredFailed int `json:"required_failed"`
	OptionalFailed int `json:"optional_failed"`
	Pending        int `json:"pending"`
	Skipped        int `json:"skipped"`
}

type documentStatsAgentFindings struct {
	Open      int `json:"open"`
	Resolved  int `json:"resolved"`
	Dismissed int `json:"dismissed"`
}

type sectionContent struct {
	Tiles     json.RawMessage    `json:"tiles,omitempty"`
	ID        string             `json:"id"`
	Title     string             `json:"title"`
	Kind      string             `json:"kind"`
	Hunks     []string           `json:"hunks"`
	Summary   json.RawMessage    `json:"summary"`
	Attention json.RawMessage    `json:"attention"`
	Evidence  []documentEvidence `json:"evidence"`
	Blocks    json.RawMessage    `json:"blocks"`
	// Assessment is absent on mechanical sections and on sections written
	// before assessments existed.
	Assessment *sectionAssessment `json:"assessment,omitempty"`
}

type sectionAssessment struct {
	Level   int                       `json:"level"`
	Factors []string                  `json:"factors"`
	Reasons []sectionAssessmentReason `json:"reasons"`
}

type sectionAssessmentReason struct {
	Text    string           `json:"text"`
	Anchors []documentAnchor `json:"anchors"`
}

type authoredDocument struct {
	Schema    string            `json:"schema"`
	Summary   json.RawMessage   `json:"summary"`
	Sections  []authoredSection `json:"sections"`
	Relations json.RawMessage   `json:"relations"`
}

type authoredSection struct {
	sectionContent
	Risks []authoredRisk `json:"risks"`
}

type documentSection struct {
	sectionContent
	Risks   []documentRisk  `json:"risks"`
	Derived documentDerived `json:"derived"`
}

type authoredRisk struct {
	Tiles    json.RawMessage       `json:"tiles,omitempty"`
	ID       string                `json:"id"`
	Tier     string                `json:"tier"`
	Category string                `json:"category"`
	Source   string                `json:"source"`
	Text     string                `json:"text"`
	Anchors  []documentAnchor      `json:"anchors"`
	Finding  optionalJSON[*string] `json:"finding,omitzero"`
}

type documentRisk struct {
	authoredRisk
	State             string `json:"state"`
	IntroducedVersion int    `json:"introduced_version"`
	MitigatedVersion  *int   `json:"mitigated_version"`
}

type documentEvidence struct {
	ID          string           `json:"id"`
	Kind        string           `json:"kind"`
	Claim       string           `json:"claim"`
	Status      string           `json:"status"`
	Granularity *string          `json:"granularity,omitempty"`
	Source      string           `json:"source"`
	Anchors     []documentAnchor `json:"anchors"`
	Detail      *string          `json:"detail,omitempty"`
	Expects     *string          `json:"expects,omitempty"`
}

type documentDerived struct {
	State           string             `json:"state"`
	RiskTier        string             `json:"risk_tier"`
	Additions       int                `json:"additions"`
	Deletions       int                `json:"deletions"`
	RoleLOC         map[string]int     `json:"role_loc"`
	Reviewers       []documentReviewer `json:"reviewers"`
	EvidenceSummary map[string]int     `json:"evidence_summary"`
	OpenFindings    int                `json:"open_findings"`
	// ReviewScore orders the sections for a reviewer (see sectionReviewScore).
	// It is absent on obsolete and unrated sections and on documents generated
	// before section scores existed.
	ReviewScore  *int                  `json:"review_score,omitempty"`
	ReviewReason *documentReviewReason `json:"review_reason,omitempty"`
}

type documentReviewReason struct {
	Risk *string `json:"risk"`
	Text string  `json:"text"`
}

type documentReviewer struct {
	Login  *string `json:"login,omitempty"`
	Team   *string `json:"team,omitempty"`
	Source string  `json:"source"`
}

type documentIndex struct {
	Hunks    []documentHunk    `json:"hunks"`
	Checks   []documentCheck   `json:"checks"`
	Reports  []documentReport  `json:"reports"`
	Comments []documentComment `json:"comments"`
	Threads  []documentThread  `json:"threads"`
	Sessions []documentSession `json:"sessions"`
	PRs      json.RawMessage   `json:"prs"`
	// Attachments are the stored description images generation was offered;
	// a version's images blocks may name only these.
	Attachments []documentAttachment `json:"attachments,omitempty"`
}

type documentAttachment struct {
	ID          string `json:"id"`
	Alt         string `json:"alt,omitempty"`
	Heading     string `json:"heading,omitempty"`
	Position    int    `json:"position"`
	ContentType string `json:"content_type"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	SourceURL   string `json:"source_url"`
}

type documentHunk struct {
	ID        string    `json:"id"`
	Path      string    `json:"path"`
	Change    string    `json:"change"`
	Role      string    `json:"role"`
	NewRange  []int     `json:"new_range"`
	Additions int       `json:"additions"`
	Deletions int       `json:"deletions"`
	GitHubURL *string   `json:"github_url,omitempty"`
	Diff      *TileHunk `json:"diff,omitempty"`
}

type documentCheck struct {
	ID          string                `json:"id"`
	Kind        string                `json:"kind"`
	Name        string                `json:"name"`
	Conclusion  string                `json:"conclusion"`
	Required    *bool                 `json:"required"`
	Head        string                `json:"head"`
	StartedAt   optionalJSON[*string] `json:"started_at,omitzero"`
	CompletedAt optionalJSON[*string] `json:"completed_at,omitzero"`
	URL         *string               `json:"url,omitempty"`
}

type documentReport struct {
	ID      string                `json:"id"`
	Tool    string                `json:"tool"`
	Marker  string                `json:"marker"`
	Comment json.RawMessage       `json:"comment,omitempty"`
	Head    optionalJSON[*string] `json:"head,omitzero"`
	Summary string                `json:"summary"`
	URL     *string               `json:"url,omitempty"`
}

type documentThread struct {
	ID       string  `json:"id"`
	Path     *string `json:"path"`
	Line     *int    `json:"line"`
	Resolved bool    `json:"resolved"`
	Outdated bool    `json:"outdated"`
	URL      *string `json:"url,omitempty"`
}

type documentAgentReview struct {
	ID           string                    `json:"id"`
	Tool         string                    `json:"tool"`
	AssessedHead string                    `json:"assessed_head"`
	Stale        bool                      `json:"stale"`
	Verdict      string                    `json:"verdict"`
	BlockedOn    *string                   `json:"blocked_on"`
	Counts       documentAgentReviewCounts `json:"counts"`
	Anchors      []documentAnchor          `json:"anchors"`
	Findings     []documentFinding         `json:"findings"`
}

type documentAgentReviewCounts struct {
	MustFix   int `json:"must_fix"`
	ShouldFix int `json:"should_fix"`
	Consider  int `json:"consider"`
}

type documentFinding struct {
	ID          string           `json:"id"`
	Severity    string           `json:"severity"`
	Text        string           `json:"text"`
	Section     *string          `json:"section"`
	Anchors     []documentAnchor `json:"anchors"`
	State       string           `json:"state"`
	AddressedBy *string          `json:"addressed_by"`
}

type documentComment struct {
	ID        json.RawMessage `json:"id"`
	Kind      string          `json:"kind"`
	Author    string          `json:"author"`
	Bot       bool            `json:"bot"`
	CreatedAt string          `json:"created_at"`
	URL       *string         `json:"url,omitempty"`
}

type documentSession struct {
	ID              string                `json:"id"`
	Vendor          string                `json:"vendor"`
	Models          []string              `json:"models,omitzero"`
	Title           optionalJSON[*string] `json:"title,omitzero"`
	GitBranch       optionalJSON[*string] `json:"git_branch,omitzero"`
	StartedAt       optionalJSON[*string] `json:"started_at,omitzero"`
	EndedAt         optionalJSON[*string] `json:"ended_at,omitzero"`
	DurationMinutes optionalJSON[*int]    `json:"duration_minutes,omitzero"`
	Outcome         optionalJSON[*string] `json:"outcome,omitzero"`
	PromptCount     *int                  `json:"prompt_count,omitempty"`
	ToolCalls       *int                  `json:"tool_calls,omitempty"`
	Restricted      bool                  `json:"restricted"`
	Prompts         []documentPrompt      `json:"prompts,omitzero"`
	Events          []documentEvent       `json:"events,omitzero"`
}

type documentPrompt struct {
	Index int    `json:"index"`
	ID    string `json:"id"`
	At    string `json:"at"`
	Text  string `json:"text"`
}

type documentEvent struct {
	ID      string `json:"id"`
	At      string `json:"at"`
	Verb    string `json:"verb"`
	Summary string `json:"summary"`
}

type documentAnchor struct {
	Type      string                `json:"type"`
	ID        json.RawMessage       `json:"id,omitempty"`
	Lines     []int                 `json:"lines,omitempty"`
	Path      *string               `json:"path,omitempty"`
	SHA       *string               `json:"sha,omitempty"`
	Number    *int                  `json:"number,omitempty"`
	Section   optionalJSON[*string] `json:"section,omitzero"`
	Session   *string               `json:"session,omitempty"`
	Index     *int                  `json:"index,omitempty"`
	Input     *string               `json:"input,omitempty"`
	Event     *string               `json:"event,omitempty"`
	Test      *string               `json:"test,omitempty"`
	System    *string               `json:"system,omitempty"`
	Key       *string               `json:"key,omitempty"`
	Page      *string               `json:"page,omitempty"`
	Assertion optionalJSON[*string] `json:"assertion,omitzero"`
	URL       *string               `json:"url,omitempty"`
	Label     *string               `json:"label,omitempty"`
}

// optionalJSON preserves the distinction between an absent property and a
// present null. Use it only for optional, nullable schema fields.
type optionalJSON[T any] struct {
	Value   T
	Present bool
}

func (o optionalJSON[T]) IsZero() bool                 { return !o.Present }
func (o optionalJSON[T]) MarshalJSON() ([]byte, error) { return json.Marshal(o.Value) }
func (o *optionalJSON[T]) UnmarshalJSON(data []byte) error {
	o.Present = true
	return json.Unmarshal(data, &o.Value)
}
func presentJSON[T any](value T) optionalJSON[T] { return optionalJSON[T]{Value: value, Present: true} }

func (a documentAnchor) identifier() string {
	var id string
	if json.Unmarshal(a.ID, &id) == nil {
		return id
	}
	return string(a.ID)
}

func documentTime(value *time.Time) optionalJSON[*string] {
	if value == nil {
		return presentJSON[*string](nil)
	}
	return presentJSON(new(value.UTC().Format(time.RFC3339Nano)))
}

func (v *DocumentValidator) decodeFullDocument(data []byte) (*reviewDocument, error) {
	value, err := decodeObject(data, "stored review")
	if err != nil {
		return nil, err
	}
	labelBlankSectionTitles(value)
	if err := v.full.Validate(value); err != nil {
		return nil, err
	}
	var doc reviewDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	// A stored version's images are checked against its own index, never the
	// current description, which may have dropped them since.
	listed := map[string]bool{}
	for _, attachment := range doc.Index.Attachments {
		listed[attachment.ID] = true
	}
	for _, section := range doc.Sections {
		var blocks []map[string]any
		if err := json.Unmarshal(section.Blocks, &blocks); err != nil {
			return nil, err
		}
		for _, block := range blocks {
			if finding := unlistedImageFinding(block, listed); finding != "" {
				return nil, errors.New(finding)
			}
		}
	}
	for i := range doc.Sections {
		doc.Sections[i].Title = SectionTitle(doc.Sections[i].Title, i)
	}
	return &doc, nil
}

func (v *DocumentValidator) encodeFullDocument(doc *reviewDocument) ([]byte, error) {
	encoded, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	// Schema validation operates on JSON values, including timestamp strings.
	var value any
	if err := json.Unmarshal(encoded, &value); err != nil {
		return nil, err
	}
	if err := v.full.Validate(value); err != nil {
		return nil, err
	}
	return encoded, nil
}

func jsonString(value string) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}
