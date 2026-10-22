package gitlocal

import (
	"context"
	"encoding/base64"
	"net/url"
	"strconv"
	"strings"

	"github.com/baum-ai/baum/internal/config"
)

// fetchCredentials carries a narrowly scoped HTTPS credential in Git's private
// child environment. It never changes Git configuration on disk or argv, and
// redirects are disabled so the header cannot reach another endpoint.
func (r *Repo) fetchCredentials(ctx context.Context, env []string) ([]string, bool, error) {
	remote, err := r.RemoteFetchURL(ctx, r.opts.Remote)
	if err != nil {
		return nil, false, err
	}
	u, err := url.Parse(remote)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return env, false, nil
	}
	host := strings.ToLower(u.Hostname())
	cfg, err := config.Load(config.Overrides{})
	if err != nil {
		return nil, false, err
	}
	token, err := cfg.ResolveGitHubToken(ctx, host)
	if err != nil {
		// Unconfigured credentials leave working user Git authentication intact.
		// Configured baum credentials that fail refresh/keyring access fail closed.
		if strings.Contains(err.Error(), config.ErrNoGitHubToken.Error()) {
			return env, false, nil
		}
		return nil, false, err
	}
	out := make([]string, 0, len(env)+20)
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_CONFIG_") || strings.HasPrefix(key, "GIT_TRACE") || key == "GIT_CURL_VERBOSE" {
			continue
		}
		out = append(out, entry)
	}
	auth := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))
	scope := "http." + u.String() + ".extraHeader"
	pairs := [][2]string{{"http.extraHeader", ""}, {scope, ""}, {scope, auth}, {"credential.helper", ""}, {"http.followRedirects", "false"}}
	out = append(out, "GIT_CONFIG_COUNT="+strconv.Itoa(len(pairs)))
	for i, pair := range pairs {
		out = append(out, "GIT_CONFIG_KEY_"+strconv.Itoa(i)+"="+pair[0], "GIT_CONFIG_VALUE_"+strconv.Itoa(i)+"="+pair[1])
	}
	return out, true, nil
}
