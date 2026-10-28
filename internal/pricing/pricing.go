// Package pricing exposes the shared price list to local application code.
package pricing

import (
	"context"
	"crypto/ed25519"
	shared "github.com/baum-ai/baum/pricing"
)

type Family = shared.Family
type Model = shared.Model
type Rate = shared.Rate
type Source = shared.Source
type Speed = shared.Speed
type Usage = shared.Usage
type Document = shared.Document
type RateSet = shared.RateSet
type Tier = shared.Tier
type Entry = shared.Entry
type List = shared.List

const FamilyClaude = shared.FamilyClaude
const FamilyGPT = shared.FamilyGPT
const FamilyGemini = shared.FamilyGemini
const FamilyGrok = shared.FamilyGrok
const FamilyUnknown = shared.FamilyUnknown
const OriginEmbedded = shared.OriginEmbedded
const OriginFetched = shared.OriginFetched
const OriginCached = shared.OriginCached
const VendorClaudeCode = shared.VendorClaudeCode
const VendorCodex = shared.VendorCodex
const SpeedUnknown = shared.SpeedUnknown
const SpeedStandard = shared.SpeedStandard
const SpeedFast = shared.SpeedFast
const SpeedUltrafast = shared.SpeedUltrafast
const PublishedURL = shared.PublishedURL
const FormatVersion = shared.FormatVersion
const SourceLiteLLM = shared.SourceLiteLLM
const SourceManual = shared.SourceManual
const ReasonBilledByCursor = shared.ReasonBilledByCursor

func Normalize(raw, vendor string) Model            { return shared.Normalize(raw, vendor) }
func Rates(model Model) (Rate, bool)                { return shared.Rates(model) }
func CurrentSource() Source                         { return shared.CurrentSource() }
func LoadPersisted(dir string) error                { return shared.LoadPersisted(dir) }
func Refresh(ctx context.Context, dir string) error { return shared.Refresh(ctx, dir) }
func Cost(model Model, usage Usage) (float64, bool) { return shared.Cost(model, usage) }
func UnpricedReason(model Model) string             { return shared.UnpricedReason(model) }
func ParseDocument(data []byte) (Document, error)   { return shared.ParseDocument(data) }
func CompareVersions(a, b string) int               { return shared.CompareVersions(a, b) }
func NewList(document Document) *List               { return shared.NewList(document) }
func EmbeddedDocument() []byte                      { return shared.EmbeddedDocument() }
func Embedded() *List                               { return shared.Embedded() }
func Active() *List                                 { return shared.Active() }
func Install(list *List)                            { shared.Install(list) }
func Version() string                               { return shared.Version() }
func Newest(a, b *List) *List                       { return shared.Newest(a, b) }
func PublishedKey() ed25519.PublicKey               { return shared.PublishedKey() }
func ParsePublished(data, signature []byte, key ed25519.PublicKey) (*List, error) {
	return shared.ParsePublished(data, signature, key)
}
