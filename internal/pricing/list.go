package pricing

import (
	"bytes"
	"crypto/ed25519"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
)

// The rates are data rather than code so a correction can reach a running
// appliance without a release. prices.json is compiled in as the baseline and
// published, signed, at PublishedURL whenever it changes on main; the jobs
// maintenance worker stores a newer published list and the API installs it.
// An appliance that never reaches the URL keeps the compiled list.
//
//go:embed prices.json
var embeddedDocument []byte

// PublishedURL is where main's prices.json is published, with its signature
// at PublishedURL + ".sig". It carries the full format (tiers, fast mode,
// 1-hour cache writes). LegacyPublishedURL keeps a flattened copy for builds
// that predate those fields and reject them as unknown.
const PublishedURL = "https://static.usebaum.com/pricing/v2/prices.json"

// LegacyPublishedURL is the pre-tier path. Its list has the same version and
// models as PublishedURL's but only the four base rates (see Document.Flatten).
const LegacyPublishedURL = "https://static.usebaum.com/pricing/prices.json"

// publishedKey verifies published lists. It is the release manifest key: the
// same party that signs a release signs its price corrections.
const publishedKey = "rU/Iy4IT2gMQlgq7qNFjliiA6tqH11nq2PC9ld4j43I="

// Entry sources. The sync tool rewrites litellm entries from LiteLLM's price
// file and never touches manual ones, which record a deliberate choice the
// source would overwrite (a Cursor rate, a peak-hour rate, a retired model).
const (
	SourceLiteLLM = "litellm"
	SourceManual  = "manual"
)

// FormatVersion marks the full format: a list that may carry cache_write_1h,
// above, fast and fast_above. The flattened copy kept for older builds omits the marker,
// so a build that reads the full format can tell the two apart even when they
// share a version.
const FormatVersion = 2

// Document is prices.json.
type Document struct {
	// Format is FormatVersion in the full format and absent in the flattened
	// copy.
	Format int `json:"format,omitzero"`
	// Version is a date, with a ".N" suffix for a second list on one day. It
	// travels with every figure computed from the list.
	Version string           `json:"version"`
	Models  map[string]Entry `json:"models"`
}

// RateSet is one set of per-million-token rates in USD.
type RateSet struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
	// CacheWrite1h prices cache writes with the 1-hour TTL; zero means the
	// set has no such rate.
	CacheWrite1h float64 `json:"cache_write_1h,omitzero"`
}

// Tier is the rate set a vendor charges for a request whose prompt is over
// PromptTokens. It replaces every rate, output included.
type Tier struct {
	PromptTokens int64   `json:"prompt_tokens"`
	Input        float64 `json:"input"`
	Output       float64 `json:"output"`
	CacheRead    float64 `json:"cache_read"`
	CacheWrite   float64 `json:"cache_write"`
	CacheWrite1h float64 `json:"cache_write_1h,omitzero"`
}

// IsZero reports whether the tier is absent (encoding/json's omitzero).
func (t Tier) IsZero() bool { return t == Tier{} }

// IsZero reports whether the set is absent (encoding/json's omitzero).
func (r RateSet) IsZero() bool { return r == RateSet{} }

// RateSet returns the tier's rates.
func (t Tier) RateSet() RateSet {
	return RateSet{Input: t.Input, Output: t.Output, CacheRead: t.CacheRead, CacheWrite: t.CacheWrite, CacheWrite1h: t.CacheWrite1h}
}

// Entry is one canonical model's rates, per million tokens. It stays
// comparable with == (pricingsync compares entries), so optional parts are
// value structs that are omitted when zero rather than pointers.
type Entry struct {
	Input        float64 `json:"input"`
	Output       float64 `json:"output"`
	CacheRead    float64 `json:"cache_read"`
	CacheWrite   float64 `json:"cache_write"`
	CacheWrite1h float64 `json:"cache_write_1h,omitzero"`
	// Above holds the rates for a request whose prompt is over
	// Above.PromptTokens.
	Above Tier `json:"above,omitzero"`
	// Fast holds the rates for a request run in fast mode.
	Fast RateSet `json:"fast,omitzero"`
	// FastAbove holds the fast-mode rates for a request whose prompt is over
	// FastAbove.PromptTokens, which is Above.PromptTokens. It needs both Above
	// and Fast. An entry with Above and Fast but no FastAbove has no published
	// fast rate over the threshold, and a fast request over it is not priced.
	FastAbove Tier   `json:"fast_above,omitzero"`
	Source    string `json:"source"`
	// LiteLLMKey names the source entry when it differs from the canonical id.
	LiteLLMKey string `json:"litellm_key,omitempty"`
	Note       string `json:"note,omitempty"`
}

// Rate returns the entry's base four rates.
func (e Entry) Rate() Rate {
	return Rate{Input: e.Input, Output: e.Output, CacheRead: e.CacheRead, CacheWrite: e.CacheWrite}
}

// Base returns the entry's standard rates.
func (e Entry) Base() RateSet {
	return RateSet{Input: e.Input, Output: e.Output, CacheRead: e.CacheRead, CacheWrite: e.CacheWrite, CacheWrite1h: e.CacheWrite1h}
}

// Flatten returns the document with only each entry's four base rates, for
// builds that predate cache_write_1h, above, fast and fast_above and reject them.
func (d Document) Flatten() Document {
	flat := Document{Version: d.Version, Models: make(map[string]Entry, len(d.Models))}
	for canonical, entry := range d.Models {
		entry.CacheWrite1h = 0
		entry.Above = Tier{}
		entry.Fast = RateSet{}
		entry.FastAbove = Tier{}
		flat.Models[canonical] = entry
	}
	return flat
}

// A same-day list is numbered from .2: the bare date is the first, and
// CompareVersions ranks YYYY-MM-DD.1 equal to it, so .1 would never install.
var versionPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}(\.([2-9]|[1-9]\d+))?$`)

// ParseDocument decodes and validates a price list. Unknown fields are
// rejected so a document written for a newer format is not half understood.
func ParseDocument(data []byte) (Document, error) {
	var document Document
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return Document{}, fmt.Errorf("decoding price list: %w", err)
	}
	if err := document.Validate(); err != nil {
		return Document{}, err
	}
	return document, nil
}

// Validate rejects a list that could not have been meant: no version, no
// models, or a rate that is negative, not finite or all zero.
func (d Document) Validate() error {
	if !versionPattern.MatchString(d.Version) {
		return fmt.Errorf("price list version %q is not YYYY-MM-DD or YYYY-MM-DD.N with N from 2", d.Version)
	}
	if d.Format != 0 && d.Format != FormatVersion {
		return fmt.Errorf("price list format %d is not understood by this build", d.Format)
	}
	if len(d.Models) == 0 {
		return errors.New("price list has no models")
	}
	for canonical, entry := range d.Models {
		if canonical == "" || canonical != strings.ToLower(strings.TrimSpace(canonical)) {
			return fmt.Errorf("price list model %q is not a canonical id", canonical)
		}
		if err := validateEntry(canonical, entry); err != nil {
			return err
		}
		if entry.Source != SourceLiteLLM && entry.Source != SourceManual {
			return fmt.Errorf("price list model %q has unknown source %q", canonical, entry.Source)
		}
	}
	return nil
}

func validateRates(canonical, name string, rates ...float64) error {
	for _, rate := range rates {
		if rate < 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
			return fmt.Errorf("price list model %q has an invalid %s rate", canonical, name)
		}
	}
	return nil
}

// unpricedBucket names a bucket the base prices that other leaves at zero. A
// tier or fast set that skipped a bucket would charge it nothing.
func unpricedBucket(base, other RateSet) string {
	switch {
	case base.Input > 0 && other.Input == 0:
		return "input"
	case base.Output > 0 && other.Output == 0:
		return "output"
	case base.CacheRead > 0 && other.CacheRead == 0:
		return "cache_read"
	case base.CacheWrite > 0 && other.CacheWrite == 0:
		return "cache_write"
	case base.CacheWrite1h > 0 && other.CacheWrite1h == 0:
		return "cache_write_1h"
	}
	return ""
}

func validateEntry(canonical string, entry Entry) error {
	if err := validateRates(canonical, "base", entry.Input, entry.Output, entry.CacheRead, entry.CacheWrite, entry.CacheWrite1h); err != nil {
		return err
	}
	if entry.Input == 0 && entry.Output == 0 {
		return fmt.Errorf("price list model %q has no input or output rate", canonical)
	}
	hasAbove, hasFast, hasFastAbove := !entry.Above.IsZero(), !entry.Fast.IsZero(), !entry.FastAbove.IsZero()
	if hasFastAbove {
		if !hasAbove || !hasFast {
			return fmt.Errorf("price list model %q has fast_above rates without both above and fast rates", canonical)
		}
		fastAbove := entry.FastAbove
		if err := validateRates(canonical, "fast_above", fastAbove.Input, fastAbove.Output, fastAbove.CacheRead, fastAbove.CacheWrite, fastAbove.CacheWrite1h); err != nil {
			return err
		}
		if fastAbove.PromptTokens != entry.Above.PromptTokens {
			return fmt.Errorf("price list model %q has fast_above prompt_tokens %d but above prompt_tokens %d", canonical, fastAbove.PromptTokens, entry.Above.PromptTokens)
		}
		if bucket := unpricedBucket(entry.Fast, fastAbove.RateSet()); bucket != "" {
			return fmt.Errorf("price list model %q prices %s in its fast rates but not in its fast_above rates", canonical, bucket)
		}
		if bucket := unpricedBucket(entry.Above.RateSet(), fastAbove.RateSet()); bucket != "" {
			return fmt.Errorf("price list model %q prices %s in its above rates but not in its fast_above rates", canonical, bucket)
		}
	}
	if hasAbove {
		above := entry.Above
		if err := validateRates(canonical, "above", above.Input, above.Output, above.CacheRead, above.CacheWrite, above.CacheWrite1h); err != nil {
			return err
		}
		if above.PromptTokens <= 0 {
			return fmt.Errorf("price list model %q has above rates without a positive prompt_tokens", canonical)
		}
		if above.Input == 0 && above.Output == 0 {
			return fmt.Errorf("price list model %q has no above input or output rate", canonical)
		}
		if bucket := unpricedBucket(entry.Base(), above.RateSet()); bucket != "" {
			return fmt.Errorf("price list model %q prices %s at its base rates but not in its above rates", canonical, bucket)
		}
	}
	if hasFast {
		fast := entry.Fast
		if err := validateRates(canonical, "fast", fast.Input, fast.Output, fast.CacheRead, fast.CacheWrite, fast.CacheWrite1h); err != nil {
			return err
		}
		if fast.Input == 0 && fast.Output == 0 {
			return fmt.Errorf("price list model %q has no fast input or output rate", canonical)
		}
		if bucket := unpricedBucket(entry.Base(), fast); bucket != "" {
			return fmt.Errorf("price list model %q prices %s at its base rates but not in its fast rates", canonical, bucket)
		}
	}
	return nil
}

// CompareVersions orders two valid versions: negative when a is older.
func CompareVersions(a, b string) int {
	dateA, seqA := splitVersion(a)
	dateB, seqB := splitVersion(b)
	if c := strings.Compare(dateA, dateB); c != 0 {
		return c
	}
	return seqA - seqB
}

func splitVersion(version string) (string, int) {
	date, suffix, found := strings.Cut(version, ".")
	if !found {
		return date, 1
	}
	seq, err := strconv.Atoi(suffix)
	if err != nil {
		return date, 1
	}
	return date, seq
}

// List is a validated price list ready to price usage.
type List struct {
	version string
	full    bool
	entries map[string]Entry
}

// NewList builds a List from a validated document.
func NewList(document Document) *List {
	entries := make(map[string]Entry, len(document.Models))
	for canonical, entry := range document.Models {
		entries[canonical] = entry
	}
	return &List{version: document.Version, full: document.Format == FormatVersion, entries: entries}
}

func (l *List) Version() string { return l.version }

// Full reports whether the list was read from the full format. A flattened
// list prices correctly only for usage without tiers, fast mode or 1-hour
// writes, so a build that reads the full format must not price with one.
func (l *List) Full() bool { return l.full }

func (l *List) rate(canonical string) (Rate, bool) {
	entry, ok := l.entries[canonical]
	return entry.Rate(), ok
}

func (l *List) entry(canonical string) (Entry, bool) {
	entry, ok := l.entries[canonical]
	return entry, ok
}

// PromptBands lists, ascending and without repeats, the prompt sizes at which
// any entry's rates change: the thresholds of its above and fast_above tiers. A
// query that sums usage groups requests by band, which keeps every request of a
// group on one side of every threshold, so the group can be priced exactly.
func (l *List) PromptBands() []int64 {
	bands := []int64{}
	for _, entry := range l.entries {
		for _, threshold := range []int64{entry.Above.PromptTokens, entry.FastAbove.PromptTokens} {
			if threshold > 0 && !slices.Contains(bands, threshold) {
				bands = append(bands, threshold)
			}
		}
	}
	slices.Sort(bands)
	return bands
}

// Models lists the canonical ids the list prices, sorted.
func (l *List) Models() []string {
	models := make([]string, 0, len(l.entries))
	for canonical := range l.entries {
		models = append(models, canonical)
	}
	slices.Sort(models)
	return models
}

var (
	embedded = mustParseEmbedded()
	active   atomic.Pointer[List]
)

func init() { active.Store(embedded) }

func mustParseEmbedded() *List {
	document, err := ParseDocument(embeddedDocument)
	if err != nil {
		panic(fmt.Sprintf("compiled prices.json: %v", err))
	}
	return NewList(document)
}

// EmbeddedDocument returns the compiled prices.json bytes.
func EmbeddedDocument() []byte { return bytes.Clone(embeddedDocument) }

// Embedded is the list compiled into this build.
func Embedded() *List { return embedded }

// Active is the list costs are priced with.
func Active() *List { return active.Load() }

// Install makes list the active list. Callers choose which list is newest;
// see Newest.
func Install(list *List) { active.Store(list) }

// Version is the active list's version. A dollar figure with no version behind
// it cannot be reproduced later, because the list it came from will have moved.
func Version() string { return Active().Version() }

// Newest returns whichever list has the later version, preferring a when they
// are equal.
func Newest(a, b *List) *List {
	if b != nil && (a == nil || CompareVersions(b.version, a.version) > 0) {
		return b
	}
	return a
}

// PublishedKey is the key published lists are signed with.
func PublishedKey() ed25519.PublicKey {
	key, err := base64.StdEncoding.DecodeString(publishedKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		panic("pricing: publishedKey is not an ed25519 public key")
	}
	return ed25519.PublicKey(key)
}

// ParsePublished accepts a published list only when key signed it and it
// still prices every model the compiled list prices. The second check stops a
// truncated or mistaken publication from turning known costs into unknowns
// across every appliance at once.
func ParsePublished(data, signature []byte, key ed25519.PublicKey) (*List, error) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(signature)))
	if err != nil {
		return nil, fmt.Errorf("decoding price list signature: %w", err)
	}
	if !ed25519.Verify(key, data, decoded) {
		return nil, errors.New("price list signature is not trusted")
	}
	document, err := ParseDocument(data)
	if err != nil {
		return nil, err
	}
	list := NewList(document)
	for canonical := range embedded.entries {
		if _, ok := list.entries[canonical]; !ok {
			return nil, fmt.Errorf("price list %s drops %q, which this build prices", list.version, canonical)
		}
	}
	return list, nil
}
