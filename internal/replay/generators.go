package replay

import (
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/baum-ai/baum/agent/prompt"
)

// GeneratorConfig is what the command line offers a generator: the settings
// the review generator takes, and Options for any other generator's own
// (--generator-option key=value). A generator refuses what it does not use.
type GeneratorConfig struct {
	Staged, Reuse bool
	// PromptOverrides are replacement prompts by id (--prompt-override).
	PromptOverrides map[string]string
	Options         map[string]string
}

var (
	generatorsMu sync.Mutex
	generators   = map[string]func(GeneratorConfig) (Generator, error){}
)

// RegisterGenerator makes a generator available to `replay --generator <name>`.
// Call it at initialisation; it panics when the name is taken.
func RegisterGenerator(name string, build func(GeneratorConfig) (Generator, error)) {
	generatorsMu.Lock()
	defer generatorsMu.Unlock()
	if _, ok := generators[name]; ok {
		panic("replay: generator " + name + " registered twice")
	}
	generators[name] = build
}

// UnregisterGenerator removes a generator, for tests that register their own.
func UnregisterGenerator(name string) {
	generatorsMu.Lock()
	defer generatorsMu.Unlock()
	delete(generators, name)
}

// GeneratorNames lists the registered generators.
func GeneratorNames() []string {
	generatorsMu.Lock()
	defer generatorsMu.Unlock()
	return slices.Sorted(maps.Keys(generators))
}

// NewGenerator builds the generator registered under name.
func NewGenerator(name string, c GeneratorConfig) (Generator, error) {
	generatorsMu.Lock()
	build, ok := generators[name]
	generatorsMu.Unlock()
	if !ok {
		return nil, fmt.Errorf("unknown generator %q; use one of %v", name, GeneratorNames())
	}
	return build(c)
}

func init() {
	RegisterGenerator("review", func(c GeneratorConfig) (Generator, error) {
		if len(c.Options) > 0 {
			return nil, fmt.Errorf("the review generator takes no --generator-option (got %v)", slices.Sorted(maps.Keys(c.Options)))
		}
		g := &ReviewGenerator{Staged: c.Staged, Reuse: c.Reuse}
		for id, text := range c.PromptOverrides {
			if g.PromptOverrides == nil {
				g.PromptOverrides = prompt.Overrides{}
			}
			g.PromptOverrides[prompt.ID(id)] = text
		}
		return g, nil
	})
}
