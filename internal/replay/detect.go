package replay

import (
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/baum-ai/baum/bundle"
)

// Detector names a structural kind of change a selection can keep or leave
// out: the cases where a specialised view of the review would apply. The
// detectors are heuristics for stratified selection, not ground truth: they
// miss changes written in ways they do not know and match some that are not
// (see Detections.Evidence for what a match rests on).
type Detector string

const (
	DetectSchema Detector = "schema"
	DetectAPI    Detector = "api"
	// DetectAny keeps cases either detector matches, DetectNone cases neither
	// does.
	DetectAny  Detector = "any"
	DetectNone Detector = "none"
)

// Valid reports a name Selection.Detector accepts; empty means no filter.
func (d Detector) Valid() bool {
	switch d {
	case "", DetectSchema, DetectAPI, DetectAny, DetectNone:
		return true
	}
	return false
}

// The kinds of evidence a match rests on. The path kinds are certain of what
// they say about the file; the others come from the text of added or removed
// lines.
const (
	EvidenceSchemaPath    = "schema:path"
	EvidenceSchemaDDL     = "schema:ddl"
	EvidenceAPISchemaFile = "api:schema-file"
	EvidenceAPIRoutesFile = "api:routes-file"
	EvidenceAPIRoute      = "api:route"
	EvidenceAPITool       = "api:tool"
)

// Detections is what the detectors found in one case's diff.
type Detections struct {
	Schema, API bool
	// Evidence lists the kinds of evidence found, sorted.
	Evidence []string
	// Blind says that no hunk of the case has patch text (it was cut or never
	// there), so the detectors could only read paths.
	Blind bool
}

// Detect runs both detectors on a diff.
func Detect(diff []bundle.Hunk) Detections {
	evidence := append(schemaEvidence(diff), apiEvidence(diff)...)
	slices.Sort(evidence)
	d := Detections{Evidence: evidence, Blind: len(diff) > 0 && !slices.ContainsFunc(diff, func(h bundle.Hunk) bool { return h.Patch != "" })}
	for _, e := range evidence {
		d.Schema = d.Schema || strings.HasPrefix(e, "schema:")
		d.API = d.API || strings.HasPrefix(e, "api:")
	}
	return d
}

// Matches reports whether the detections satisfy the filter.
func (d Detections) Matches(want Detector) bool {
	switch want {
	case DetectSchema:
		return d.Schema
	case DetectAPI:
		return d.API
	case DetectAny:
		return d.Schema || d.API
	case DetectNone:
		return !d.Schema && !d.API
	}
	return true
}

// reviewable reports a hunk the detectors look at: its role is not one of
// those that merely mention a change (tests, docs) or copy it (generated and
// vendored files).
func reviewable(h bundle.Hunk) bool {
	switch h.Role {
	case bundle.RoleTest, bundle.RoleDocs, bundle.RoleGenerated, bundle.RoleVendored:
		return false
	}
	return true
}

// changedLines returns the added and removed lines of a hunk's patch, without
// their markers, leaving out the ones that are comments.
func changedLines(patch string) []string {
	var lines []string
	for _, line := range strings.Split(patch, "\n") {
		if !strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "-") {
			continue
		}
		text := line[1:]
		trimmed := strings.TrimSpace(text)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "/*") ||
			strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "<!--") || strings.HasPrefix(trimmed, "--") {
			continue
		}
		lines = append(lines, text)
	}
	return lines
}

// A DDL phrase counts when it reads as a statement: the object is named and
// what follows is what follows in SQL, so prose and labels that use the same
// words ("create a table of values", "Drop table?") do not match.
const (
	ident = "[\\w.\"`\\[\\]]+"
	end   = "\\s*(?:\\(|;|$|\"|'|`|\\bas\\b|\\bon\\b|\\busing\\b|\\bfor\\b|\\breturns\\b|\\bcascade\\b|\\brestrict\\b|\\))"
)

var ddl = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bcreate\s+(?:or\s+replace\s+)?(?:unique\s+)?(?:(?:temp|temporary|unlogged)\s+)?(?:table|index|view|materialized\s+view|type|trigger|function|extension|sequence|schema|domain|policy)\s+(?:if\s+not\s+exists\s+)?` + ident + end),
	regexp.MustCompile(`(?i)\balter\s+(?:table|type|index|view|sequence|schema)\s+(?:if\s+exists\s+)?(?:only\s+)?` + ident + `\s+(?:add|drop|alter|rename|set|owner|enable|disable|validate|attach|detach)\b`),
	regexp.MustCompile(`(?i)\bdrop\s+(?:table|index|view|type|trigger|function|extension|sequence|schema|policy)\s+(?:if\s+exists\s+)?` + ident + end),
	regexp.MustCompile(`(?i)\b(?:drop|add)\s+(?:column|constraint)\s+(?:if\s+(?:not\s+)?exists\s+)?` + ident),
}

// SchemaChange reports a change to a database schema: a hunk outside tests,
// docs, generated and vendored files that is a migration or a .sql file (which
// covers queries/*.sql), or that adds or removes a DDL statement.
func SchemaChange(diff []bundle.Hunk) bool { return len(schemaEvidence(diff)) > 0 }

func schemaEvidence(diff []bundle.Hunk) []string {
	found := map[string]bool{}
	for _, h := range diff {
		if !reviewable(h) {
			continue
		}
		lower := strings.ToLower(h.Path)
		if strings.HasSuffix(lower, ".sql") || h.Role == bundle.RoleMigration || hasSegment(lower, "migrations") {
			found[EvidenceSchemaPath] = true
		}
		for _, line := range changedLines(h.Patch) {
			if slices.ContainsFunc(ddl, func(re *regexp.Regexp) bool { return re.MatchString(line) }) {
				found[EvidenceSchemaDDL] = true
			}
		}
	}
	return keys(found)
}

func keys(set map[string]bool) []string {
	var out []string
	for k := range set {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func hasSegment(lowerPath, segment string) bool {
	for _, s := range strings.Split(lowerPath, "/") {
		if s == segment {
			return true
		}
	}
	return false
}

const verbs = `get|post|put|patch|delete|head|options|any`

var (
	// A route is registered on a router, a mux, an app or a group, never on a
	// client: a verb method counts only on a receiver named like one and with a
	// path, and Handle and HandleFunc with a path; decorators and annotations
	// are always server-side.
	routeLine = regexp.MustCompile(`(?i)` +
		`\b(?:r|e|g|rt|app|api|srv|v\d+|\w*(?:router|mux|server|group|engine|routes?|handler))\.(?:` + verbs + `)\(\s*["'` + "`" + `](?:[A-Za-z]+\s+)?/` +
		`|\.(?:handle|handlefunc)\(\s*["` + "`" + `][^"` + "`" + `]*/` +
		`|\.(?:route|mount)\(\s*["'` + "`" + `]/` +
		`|@\w+\.(?:` + verbs + `|route)\(` +
		`|@(?:get|post|put|patch|delete|request)mapping\b`)
	toolLine   = regexp.MustCompile(`(?i)\b(?:newtool|addtool|registertool|definetool)\s*\(|^\s*@tool\b`)
	schemaLine = regexp.MustCompile(`(?i)["']?(?:input_schema|inputschema)["']?\s*[:=]`)
	nameLine   = regexp.MustCompile(`(?i)\bname["']?\s*[:=]`)
)

// APIChange reports a change to a published interface: a hunk outside tests,
// docs, generated and vendored files that adds or removes a route
// registration or an agent tool definition, or that touches a published JSON
// schema, an OpenAPI description or a Rails routes file.
func APIChange(diff []bundle.Hunk) bool { return len(apiEvidence(diff)) > 0 }

func apiEvidence(diff []bundle.Hunk) []string {
	found := map[string]bool{}
	for _, h := range diff {
		if !reviewable(h) {
			continue
		}
		lower := strings.ToLower(h.Path)
		switch {
		case publishedSchemaPath(lower):
			found[EvidenceAPISchemaFile] = true
		case path.Base(lower) == "routes.rb":
			found[EvidenceAPIRoutesFile] = true
		}
		lines := changedLines(h.Patch)
		// An input_schema is a tool definition when it sits beside the tool's
		// name; on its own it is as likely an argument of a call to a model.
		named := slices.ContainsFunc(lines, nameLine.MatchString)
		for _, line := range lines {
			switch {
			case routeLine.MatchString(line):
				found[EvidenceAPIRoute] = true
			case toolLine.MatchString(line), named && schemaLine.MatchString(line):
				found[EvidenceAPITool] = true
			}
		}
	}
	return keys(found)
}

func publishedSchemaPath(lowerPath string) bool {
	base := path.Base(lowerPath)
	switch {
	case strings.HasSuffix(base, ".schema.json"):
		return true
	case strings.HasPrefix(base, "openapi.") || strings.HasPrefix(base, "swagger."):
		return true
	case strings.HasSuffix(base, ".json"):
		return hasSegment(path.Dir(lowerPath), "schema") || hasSegment(path.Dir(lowerPath), "schemas")
	}
	return false
}
