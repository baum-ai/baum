# Legacy appliance API client

This package retains the appliance HTTP client and protocol constants for
existing integrations. The desktop CLI has no hosted authentication or upload
commands and does not link this client.

## CLI protocol

Requests name the client protocol in `X-Baum-CLI-Protocol`. Responses name the
supported range in `X-Baum-CLI-Protocol-Min` and `X-Baum-CLI-Protocol-Max`. A
client outside that range stops with an error identifying which side needs
updating. A response without a range counts as protocol 1, except a 5xx
response, which may come from a proxy rather than the appliance.

`Protocol` changes when the client contract changes, including the supported
upload manifest version. The appliance retains these constants for older
clients; removing desktop CLI commands does not remove its API endpoints.
