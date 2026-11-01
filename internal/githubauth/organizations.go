package githubauth

import (
	"context"
	"crypto/sha256"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ApprovalURL is the OAuth App's page in a member's GitHub settings, where
// they can ask an organization's owners to approve it.
const ApprovalURL = "https://github.com/settings/connections/applications/" + ClientID

const (
	organizationsTTL   = 5 * time.Minute
	organizationPages  = 3
	organizationsLimit = 1 << 20
)

// OrganizationAccess lists the organizations the signed-in person belongs to
// but this authorization cannot reach, which is how GitHub shows one that
// restricts OAuth app access until an owner approves the app. The list is
// best effort: it is empty when GitHub could not be asked.
type OrganizationAccess struct {
	Hidden      []string `json:"hidden"`
	ApprovalURL string   `json:"approval_url"`
}

// Organizations compares the person's active organization memberships
// (`GET /user/memberships/orgs`) with the organizations GitHub lets an OAuth
// app token operate on (`GET /user/orgs`). Both need only the `read:org`
// scope the sign-in already holds. GitHub documents the second list as
// limited to organizations the authorization allows acting on, so a member
// of an organization that has not approved the app appears only in the first.
// Results are cached for five minutes per token; refresh asks again.
func (m *Manager) Organizations(ctx context.Context, token string, refresh bool) (OrganizationAccess, error) {
	key := sha256Sum(token)
	m.orgsMu.Lock()
	defer m.orgsMu.Unlock()
	if !refresh && !m.orgsAt.IsZero() && m.orgsKey == key && m.now().Sub(m.orgsAt) < organizationsTTL {
		return m.orgs, nil
	}
	memberships, err := m.organizationLogins(ctx, token, "/user/memberships/orgs", true)
	if err != nil {
		return OrganizationAccess{}, err
	}
	reachable, err := m.organizationLogins(ctx, token, "/user/orgs", false)
	if err != nil {
		return OrganizationAccess{}, err
	}
	seen := map[string]bool{}
	for _, login := range reachable {
		seen[strings.ToLower(login)] = true
	}
	out := OrganizationAccess{Hidden: []string{}, ApprovalURL: ApprovalURL}
	for _, login := range memberships {
		if !seen[strings.ToLower(login)] {
			out.Hidden = append(out.Hidden, login)
		}
	}
	sort.Strings(out.Hidden)
	m.orgs, m.orgsKey, m.orgsAt = out, key, m.now()
	return out, nil
}

func (m *Manager) organizationLogins(ctx context.Context, token, path string, memberships bool) ([]string, error) {
	var logins []string
	for page := 1; page <= organizationPages; page++ {
		query := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}
		if memberships {
			query.Set("state", "active")
		}
		var rows []struct {
			Login        string `json:"login"`
			Organization struct {
				Login string `json:"login"`
			} `json:"organization"`
		}
		if _, err := m.requestLimit(ctx, m.api+path+"?"+query.Encode(), nil, token, &rows, organizationsLimit); err != nil {
			return nil, err
		}
		for _, r := range rows {
			login := r.Login
			if memberships {
				login = r.Organization.Login
			}
			if login != "" {
				logins = append(logins, login)
			}
		}
		if len(rows) < 100 {
			break
		}
	}
	return logins, nil
}

func sha256Sum(s string) [sha256.Size]byte { return sha256.Sum256([]byte(s)) }
