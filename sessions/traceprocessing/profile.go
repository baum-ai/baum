package traceprocessing

// MappingProfile selects the established product-specific interpretation of
// transcript evidence. Local is the default; Hosted preserves appliance output.
// It must be passed per build, never selected through global mutable state.
type MappingProfile uint8

const (
	Local MappingProfile = iota
	Hosted
)
