package tiles

// BuiltinType describes a built-in tile type.
type BuiltinType struct {
	// Name is the tile's type, and the ID of a single tile.
	Name  string
	Title string
	// UseWhen is the one line a composer reads to decide whether to use it.
	UseWhen string
	// Single types have at most one tile per document, whose ID is the type
	// name.
	Single bool
	Size   Size
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

// TypeCustom is reserved for a tile that embeds its own type definition. This
// version accepts its shape but never renders one: it is always unavailable.
const TypeCustom = "custom"

// Builtins lists the built-in tile types in catalogue order.
var Builtins = []BuiltinType{
	{TypeOverview, "Overview", "Always: what the pull request does, its kind, what needs attention and the release note.", true, SizeHalf},
	{TypeChange, "Change", "One per logical change: its summary, behaviour, risks, evidence and the hunks it covers.", false, SizeThird},
	{TypeRisks, "Risks", "Always: the active risks of the current changes, most severe first.", true, SizeHalf},
	{TypeEvidence, "Evidence", "Always: what tests, checks, runs and reviews show for each change.", true, SizeThird},
	{TypeIntent, "Intent", "When the pull request states what it implements or a change states its intent.", true, SizeThird},
	{TypeDecisions, "Decisions", "When people or agents made design decisions worth reviewing.", true, SizeThird},
	{TypeContext, "Context", "How the change fits into the codebase around it.", true, SizeThird},
	{TypeMedia, "Media", "When the pull request carries screenshots, recordings or other media.", true, SizeThird},
	{TypeEverythingElse, "Everything else", "When there is a mechanical change or hunks no change holds.", true, SizeThird},
	{TypeProse, "Notes", "A point that needs prose and fits no other tile.", false, SizeHalf},
	{TypeTable, "Table", "Values that compare best in rows and columns.", false, SizeHalf},
	{TypeDiagram, "Diagram", "A flow or structure that reads best as a diagram.", false, SizeHalf},
	{TypeCode, "Code", "A short excerpt of code worth reading in full.", false, SizeHalf},
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
