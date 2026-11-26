# Linear desktop connection

The local daemon authorizes the desktop user with Linear's read scope, using
OAuth authorization code flow with S256 PKCE. Register the exact redirect
`http://127.0.0.1:37841/oauth/linear/callback` in the Linear OAuth application's
settings. Development and packaged builds use Baum's public client ID by
default. The release repository variable `BAUM_LINEAR_CLIENT_ID` overrides it
at build time. A daemon environment variable of the same name takes precedence
at startup.
The client secret is unnecessary for authorization and refresh.

The loopback listener runs only during sign-in, for up to five minutes. The
daemon stores the selected workspace and rotating credential together under
`baum.linear.app` in the OS credential store. Disconnect clears the local
credential and issue cache. To revoke the grant at Linear, remove baum in
Linear's application settings.

Issue lookup recognizes explicit Linear issue URLs for the connected workspace
in a PR description. Only issue identifiers are sent to Linear. Successful
lookups are cached in daemon memory for five minutes and cleared on account
changes. Run `just oss-go test -race ./internal/integrations/...` to exercise
callback handling, refresh and lookup with a simulated Linear API.
