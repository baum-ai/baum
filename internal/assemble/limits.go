package assemble

import (
	"github.com/baumai/baum/bundle"
	"github.com/baumai/baum/internal/config"
)

// Limits are the caps one assembly runs within.
type Limits struct {
	Caps     bundle.Caps
	Checkout bundle.CheckoutConfig
	// MaxImages and MaxImageBytes bound the description images downloaded.
	MaxImages     int
	MaxImageBytes int64
}

// DefaultLimits are the limits with no override in the config file.
func DefaultLimits() Limits {
	return LimitsFrom(config.CapDefaults)
}

// LimitsFrom reads limits from caps by the names of config.CapDefaults. A
// name missing from caps is zero, which the bundle package reads as its own
// "unlimited" or "none" for that cap, so pass config.Config.EffectiveCaps.
func LimitsFrom(caps map[string]int) Limits {
	return Limits{
		Caps: bundle.Caps{
			MaxSessions:                caps["max_sessions"],
			MaxPromptsPerSession:       caps["max_prompts_per_session"],
			MaxCommandEventsPerSession: caps["max_command_events_per_session"],
			MaxComments:                caps["max_comments"],
			MaxHunkPatchLines:          caps["max_hunk_patch_lines"],
			MaxBytes:                   caps["max_bytes"],
			MaxDiffIndexBytes:          caps["max_diff_index_bytes"],
			MaxDiffFiles:               caps["max_diff_files"],
			MaxDiffPatchBytes:          int64(caps["max_diff_patch_bytes"]),
			MaxImageAltBytes:           caps["max_image_alt_bytes"],
		},
		Checkout: bundle.CheckoutConfig{
			MaxFileBytes:  int64(caps["checkout_max_file_bytes"]),
			MaxTotalBytes: int64(caps["checkout_max_total_bytes"]),
			MaxTotalFiles: caps["checkout_max_total_files"],
		},
		MaxImages:     caps["max_images"],
		MaxImageBytes: int64(caps["max_image_bytes"]),
	}
}
