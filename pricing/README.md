# pricing

The shared price-list implementation used by the local product and the hosted
platform. `prices.json` is the single compiled baseline. The platform's
pricing-sync command updates it and the `pricing-publish` workflow signs it and
publishes it at `PublishedURL` (`static.usebaum.com/pricing/v3/prices.json`,
signature at `.sig`). Local refresh/cache handling and backend cost accounting
use the same `List` and active-list state as the core token-cost calculations.

Published lists are verified against `PublishedKey`, the price-list key, which
signs nothing else; a list signed by any other key fails with
`ErrSignatureNotTrusted`. The path version changes whenever older builds could
not read the publication: v2 added the full format, and v3 moved to the
price-list key. Builds before v3, which includes every appliance released
before the change, fetch v2 and trust the release manifest key. While those are
supported, `pricing-publish` also signs the same file with the manifest key and
publishes it at v2. When the transition ends, v2 stops changing and keeps its
last signed pair, as v1 did.

Accounting prices each attempt using its reported model, or its configured
model when the actual model was not reported. A total using any configured
model carries `estimated_from_configured_model`. Agent costs (`OutcomeCost`,
`AccountingCost`) estimate rather than stay unknown: an attempt with no tokens
costs nothing, an incomplete one is priced from what it reported, and a missing
speed, cache TTL or prompt tier takes the lowest rate the usage could have been
billed at (standard speed, the 5-minute write, the base tier). The reason then
names what was missing (`usage_incomplete`, `model_or_prompt_rates_unknown`,
`speed_not_reported`, `cache_ttl_not_reported`) and the cost is a lower bound.
An attempt with no model or no rates is left out of the sum; the cost is nil
only when no attempt can be priced. `Cost` itself stays exact: it reports not
priced rather than guess, for the hosted ledger and analytics.

`oss/internal/pricing` is an import facade for local consumers; it owns no
price list or mutable state. Hosted fixture-model coverage stays in the
platform so the standalone module does not need hosted fixtures.
