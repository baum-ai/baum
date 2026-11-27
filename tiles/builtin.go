package tiles

// BuiltinType describes a built-in tile type.
type BuiltinType struct {
	// Name is the tile's type, and the ID of a single tile.
	Name  string
	Title string
	// UseWhen is the one line that says what the type is for.
	UseWhen string
	// Single types have at most one tile per document, whose ID is the type
	// name.
	Single bool
}

// The built-in tile types.
const (
	TypeOverview       = "overview"
	TypeChange         = "change"
	TypeRisks          = "risks"
	TypeEvidence       = "evidence"
	TypeIntent         = "intent"
	TypeDecisions      = "decisions"
	TypeContext        = "context"
	TypeMedia          = "media"
	TypeEverythingElse = "everything_else"
	TypeProse          = "prose"
	TypeTable          = "table"
	TypeDiagram        = "diagram"
	TypeCode           = "code"
)

// TypeCustom is the type of a tile that embeds its own type definition
// (TypeDefinition): a component, the schema of its data and how to fill it.
const TypeCustom = "custom"

// Builtins lists the built-in tile types in catalogue order.
var Builtins = []BuiltinType{
	{TypeOverview, "Overview", "Always: what the pull request does, its kind, what needs attention and the release note.", true},
	{TypeChange, "Change", "One per logical change: its summary, behaviour, risks, evidence and the hunks it covers.", false},
	{TypeRisks, "Risks", "Always: the active risks of the current changes, most severe first.", true},
	{TypeEvidence, "Evidence", "Always: what tests, checks, runs and reviews show for each change.", true},
	{TypeIntent, "Intent", "When the pull request states what it implements or a change states its intent.", true},
	{TypeDecisions, "Decisions", "When people or agents made design decisions worth reviewing.", true},
	{TypeContext, "Context", "How the change fits into the codebase around it.", true},
	{TypeMedia, "Media", "When the pull request carries screenshots, recordings or other media.", true},
	{TypeEverythingElse, "Everything else", "When there is a mechanical change or hunks no change holds.", true},
	{TypeProse, "Notes", "A point that needs prose and fits no other tile.", false},
	{TypeTable, "Table", "Values that compare best in rows and columns.", false},
	{TypeDiagram, "Diagram", "A flow or structure that reads best as a diagram.", false},
	{TypeCode, "Code", "A short excerpt of code worth reading in full.", false},
}

var builtinsByName = func() map[string]BuiltinType {
	byName := make(map[string]BuiltinType, len(Builtins))
	for _, builtin := range Builtins {
		byName[builtin.Name] = builtin
	}
	return byName
}()

// Builtin returns the built-in type with the given name.
func Builtin(name string) (BuiltinType, bool) {
	builtin, ok := builtinsByName[name]
	return builtin, ok
}
