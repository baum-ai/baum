# GitHub device authentication

This package owns the daemon's GitHub.com connection, local authentication API
and token resolution. The desktop sign-in UI is implemented separately. `Manager` owns one active
device authorization and one selected account. Its public `Device` and `Status`
values exclude tokens and the device secret. `Credential` and `Token` are only
for trusted daemon code, never API responses, logs or model environments.

The public OAuth client ID is `Ov23liFSMjrhof9WQlOi`. Device flow requests `repo`
for private repositories and `read:org` for team review requests. The browser
uses GitHub's fixed device verification page. HTTP exchanges have a 15-second
limit, a 64 KiB response limit, no redirects, and no upstream error text. Poll
performs one exchange, enforces the server interval, and adds five seconds on
`slow_down`. Callers should poll with a request context and stop at terminal
states. Starting again replaces the previous pending authorization.

The OAuth App has token expiration turned off, so a grant has no expiry and no
refresh token, and the credential lasts until the user revokes it. GitHub
refreshes an OAuth App's expiring tokens only with the client secret, which a
desktop binary cannot keep, so expiring grants from before that change are
refreshed with the public client ID and refresh token, without a client
secret, and any refusal asks the user to reconnect. Refresh is serialized with
account changes. `/user` checks
account identity and OAuth scopes before persistence. Rotated credentials must
be saved before the new access token is returned. An unavailable store fails
closed, with no plaintext fallback. Disconnect deletes the local credential;
it does not revoke authorization on GitHub.

`ProcessStore` uses macOS Keychain, Windows Credential Manager, or Linux Secret
Service through the pure-Go `go-keyring` package. The host is the service
namespace (`baum.github.com`); the selected account and tokens occupy one atomic
item (`desktop-account`), so replacing accounts leaves no previous account item.
Enterprise device authorization and multiple simultaneous accounts are outside
this first version. Native keyring operations run in the hidden
`baum internal github-credential`
helper with a 20-second deadline. Before reading a request or opening the
keyring, the helper verifies its parent's executable identity using operating
system process metadata and requires pipe input and output. A shell or coding
agent invoking the hidden command directly is refused, including save and
delete requests. Credentials pass through private pipes. This trusts the
installed baum executable and the OS process metadata; unsupported operating
systems fail closed. It does not isolate credentials from arbitrary native
code that can already access the user's keyring or modify the installed binary.
Cancellation kills the helper process group (including macOS security), waits
for process termination, then releases the manager lock. Lock acquisition is
also cancellable. Native keyring code is private to the helper. No detached
saves can race disconnect. Rotated grants remain in daemon memory until
validation and persistence succeed; retries never reuse the spent refresh token.
Lifetimes derive from the original token receipt time. If a retained access
token nears expiry during an outage, its latest refresh token obtains a new
pair before validation and persistence resume. A daemon crash before successful
persistence can require reconnecting; credentials are never cached on disk.

Run `just oss-go test -race ./internal/githubauth` from the repository root.

The daemon shares one manager with configuration, GitHub HTTP clients and the
local authentication controller. Explicit environment/config credentials take
precedence. No GitHub CLI executable, token store or host registry is consulted.
Enterprise credentials require an explicit matching `GH_HOST`; device sign-in
is GitHub.com only. Cached GraphQL clients resolve credentials on every request;
a discovery reader aborts if its credential partition changes mid-transaction.

The local `/api/github-auth` namespace uses the existing Host, session, Origin
and CSRF boundary. GET observes safe status (`state`, `host`, `login`, `source`,
`override`, `saved`, optional public `pending`, `error`, `message`). POST `/start`,
`/cancel`, `/disconnect` and `/retry` mutate the connection. Go owns token polling;
the browser never receives access/refresh tokens or device secrets. `/retry`
rechecks a recovered credential store without starting a new authorization.
Error codes include `credential_store_unavailable`, `auth_request_failed`,
`reconnect_required`, `insufficient_scopes`, `sso_required` and
`permission_denied`. Terminal device states include denied, expired and cancelled.

Automatic scheduling is fenced while device sign-in is pending and resumes on
cancel/error expiry. An effective account change establishes fresh PR baselines
without generating historical work. Existing preferences, daily spending limits,
pauses and saved reviews remain. In-flight discovery is cancelled and obsolete
snapshots/observations cannot enter the new account's partition. Final review
publication using the saved GitHub.com account is serialized against account
replacement. Reviews using explicit environment/config or Enterprise credentials
keep running and may publish when the unrelated saved account changes.
Persistence attempts are conservatively treated as identity changes even on
failure: a native helper may
commit a write before its reply is lost. A failed write may require retrying the
store or reconnecting; baum never assumes the durable state was unchanged.

HTTPS Git fetches use the selected host credential through Git's private child
environment, scoped to the exact remote URL. Credential helpers are disabled for
that fetch, redirects are refused, and neither argv nor disk configuration holds
the token. Authenticated Git failure messages discard upstream stderr. SSH
fetches retain the user's working configuration. Native credential-store and
live GitHub OAuth smoke tests still require each supported OS and a real account.

The same token also lists the account's repositories for picking
(`github.Client.ViewerRepositories`, GraphQL to the host's API endpoint, needing
only the `repo read:org` sign-in) and clones a picked repository
(`gitlocal.Clone`, HTTPS to github.com with the same private-environment header
as a fetch). A listing failure maps to the connection error codes above plus
`not_connected` and `rate_limited`, using the runtime health this package keeps.
See [`internal/githubrepos`](../githubrepos/README.md).

Queued review confirmations include the credential partition inside the existing
configuration digest. A job admitted just before switching accounts cannot start
with the replacement account, including after daemon restart. Baum-issued
credentials use the validated account ID for their private partition, so
same-account refresh preserves queued confirmations. Explicit overrides use a
token fingerprint and require a fresh estimate when replaced. A discovery reader
still refuses an exact token change mid-transaction and retries with a new reader;
a late old-token identity lookup cannot inherit a replacement account's ID.
