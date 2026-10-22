# GitHub device authentication

This package is the daemon foundation for GitHub.com authentication. It is not
connected to the desktop UI or token resolution yet. `Manager` owns one active
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

Refresh is serialized with account changes. Expiring tokens are refreshed with
the public client ID and refresh token, without a client secret. `/user` checks
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
