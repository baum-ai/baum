package replay

import (
	"path"
	"regexp"
	"strings"

	"github.com/baum-ai/baum/bundle"
)

// Detector names a structural kind of change a selection can keep or leave
// out: the cases where a specialised view of the review would apply.
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

// Detections is what the detectors found in one case's diff.
type Detections struct{ Schema, API bool }

// Detect runs both detectors on a diff.
func Detect(diff []bundle.Hunk) Detections {
	return Detections{Schema: SchemaChange(diff), API: APIChange(diff)}
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
// their markers.
func changedLines(patch string) []string {
	var lines []string
	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
			lines = append(lines, line[1:])
		}
	}
	return lines
}

var ddl = regexp.MustCompile(`(?i)\b(create\s+(or\s+replace\s+)?(unique\s+)?(table|index|view|materialized\s+view|type|trigger|function|extension|sequence|schema|domain|policy)` +
	`|alter\s+(table|type|index|view|sequence|schema)` +
	`|drop\s+(table|index|view|type|trigger|function|extension|sequence|schema|column|constraint|policy)` +
	`|add\s+(column|constraint))\b`)

// SchemaChange reports a change to a database schema: a hunk outside tests,
// docs, generated and vendored files that adds or removes a DDL statement, or
// that is a migration or a .sql file (which covers queries/*.sql).
func SchemaChange(diff []bundle.Hunk) bool {
	for _, h := range diff {
		if !reviewable(h) {
			continue
		}
		lower := strings.ToLower(h.Path)
		if strings.HasSuffix(lower, ".sql") || h.Role == bundle.RoleMigration || hasSegment(lower, "migrations") {
			return true
		}
		for _, line := range changedLines(h.Patch) {
			if ddl.MatchString(line) {
				return true
			}
		}
	}
	return false
}

func hasSegment(lowerPath, segment string) bool {
	for _, s := range strings.Split(lowerPath, "/") {
		if s == segment {
			return true
		}
	}
	return false
}

// apiLine matches an added or removed line that registers an HTTP route or
// defines a tool an agent can call.
var apiLine = regexp.MustCompile(`(?i)` +
	`\.(handle|handlefunc)\(\s*["` + "`" + `][^"` + "`" + `]*/` +
	`|\.(get|post|put|patch|delete|head|options)\(\s*["'` + "`" + `]/` +
	`|@(app|router|bp|blueprint)\.(get|post|put|patch|delete|route)\(` +
	`|\.(route|mount)\(\s*["'` + "`" + `]/` +
	`|\b(newtool|addtool|registertool|definetool)\s*\(` +
	`|["']?(input_schema|inputschema)["']?\s*[:=]` +
	`|^\s*@tool\b`)

// APIChange reports a change to a published interface: a hunk outside tests,
// docs, generated and vendored files that adds or removes a route
// registration or an agent tool definition, or that touches a published JSON
// schema or an OpenAPI description (a .json file under a schema or schemas
// directory, *.schema.json, openapi and swagger files).
func APIChange(diff []bundle.Hunk) bool {
	for _, h := range diff {
		if !reviewable(h) {
			continue
		}
		if publishedSchemaPath(strings.ToLower(h.Path)) {
			return true
		}
		for _, line := range changedLines(h.Patch) {
			if apiLine.MatchString(line) {
				return true
			}
		}
	}
	return false
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
