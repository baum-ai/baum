# Troubleshooting GitHub access

The desktop app signs in with GitHub's device flow, as the baum OAuth App. It
asks for `repo` (private repositories) and `read:org` (team review requests).
Two organization settings can hide repositories from that sign-in.

baum's page in your GitHub settings is
<https://github.com/settings/connections/applications/Ov23liFSMjrhof9WQlOi>.
It shows which organizations have granted baum access and lets you request it
where they have not.

## An organization restricts OAuth app access

**What you see.** You are signed in, but an organization's repositories are
missing from setup step 1, from Add repositories and from the Repositories
page. A banner says "baum can't see repositories in *name*" and links to
GitHub.

**Why.** An organization that restricts OAuth app access hides its repositories
from any OAuth App an owner has not approved. Signing in does not change that.
New organizations restrict access by default.

**Fix.**

1. Open the link in the banner (baum's page in your GitHub settings).
2. Next to the organization, choose **Request**. An owner gets the request.
   If you own the organization, approve baum under its **Settings → Third-party
   access** instead.
3. When the owner approves, go back to baum and choose **Check again**.

The banner lists organizations you belong to that GitHub hides from baum. If
baum uses a token from `GITHUB_TOKEN`, `GH_TOKEN` or the config file instead of
its own sign-in, it does not check organizations and shows no banner.

## An organization requires SAML single sign-on

**What you see.** baum says an organization requires GitHub SSO, with an
**Authorize baum on GitHub** link. It can appear right after you sign in, or
later when baum next reads that organization.

**Fix.** Open the link and authorize baum for the organization (GitHub shows
an **Authorize** button for organizations that enforce SAML), complete its single
sign-on, then choose **Check again** in baum. If GitHub's page offers no such
button, open the organization's repository in your browser and follow GitHub's
SSO prompt.
