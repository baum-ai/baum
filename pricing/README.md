# pricing

The shared price-list implementation used by the local product and the hosted
platform. `prices.json` is the single compiled baseline. The platform's
pricing-sync command updates it and the publishing workflow signs it at the
existing v2 URL. Local refresh/cache handling and backend estimates use the
same `List` and active-list state as the core token-cost calculations.

`oss/internal/pricing` is an import facade for local consumers; it owns no
price list or mutable state. Hosted fixture-model coverage stays in the
platform so the standalone module does not need hosted fixtures.
