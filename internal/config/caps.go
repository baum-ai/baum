package config

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// CapDefaults are the generation bundle caps by name, which the [caps] table
// of the config file overrides. The values are the hosted service's defaults,
// so the same pull request is trimmed the same way in both places. Byte caps
// are bytes; the rest are counts or line counts.
var CapDefaults = map[string]int{
	"max_sessions":                   12,
	"max_prompts_per_session":        2,
	"max_command_events_per_session": 20,
	"max_comments":                   40,
	"max_hunk_patch_lines":           200,
	"max_bytes":                      262144,
	"max_diff_files":                 3000,
	"max_diff_patch_bytes":           100 << 20,
	"max_diff_index_bytes":           512 << 10,
	"max_images":                     20,
	"max_image_bytes":                10 << 20,
	"max_image_alt_bytes":            512,
	"checkout_max_file_bytes":        1 << 20,
	"checkout_max_total_bytes":       200 << 20,
	"checkout_max_total_files":       20000,
}

// CapNames lists the accepted cap names, sorted.
func CapNames() []string {
	names := make([]string, 0, len(CapDefaults))
	for name := range CapDefaults {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// EffectiveCaps returns every cap: the defaults with the configured
// overrides applied. An unknown name or a negative value is an error, so a
// misspelt override is not silently ignored.
func (c Config) EffectiveCaps() (map[string]int, error) {
	caps := make(map[string]int, len(CapDefaults))
	for name, value := range CapDefaults {
		caps[name] = value
	}
	names := make([]string, 0, len(c.Caps))
	for name := range c.Caps {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if _, known := CapDefaults[name]; !known {
			return nil, fmt.Errorf("config: unknown cap %q in [caps]; known caps: %s", name, strings.Join(CapNames(), ", "))
		}
		if c.Caps[name] < 0 {
			return nil, fmt.Errorf("config: cap %q must not be negative", name)
		}
		caps[name] = c.Caps[name]
	}
	return caps, nil
}
