package gitlocal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// RetainPrefix is the namespace of the refs that keep the commits of saved
// review versions in the clone, so git gc never prunes them while their
// versions exist: RetainPrefix + <repository>/<pr>/<version>/<role>.
// <repository> is the store's lowercased repository key (owner/name, or
// host/owner/name) as one component, every byte but a-z, 0-9, '-' and '_'
// written as %XX; <role> is head, base or merge-base. Nothing else under
// refs/baum/ is ever written or removed through it.
const RetainPrefix = "refs/baum/reviews/"

// RetainRoles are the commits of a version a retained ref can name.
var RetainRoles = []string{"head", "base", "merge-base"}

// maxRetainListBytes bounds the listing of the retained refs.
const maxRetainListBytes = 64 << 20

// RetainedName is what a retained ref's name says.
type RetainedName struct {
	Repository string
	Number     int
	Version    int
	Role       string
}

// Ref is the ref's full name.
func (n RetainedName) Ref() string {
	return RetainVersionPrefix(n.Repository, n.Number, n.Version) + n.Role
}

// RetainVersionPrefix is the directory of a version's retained refs, ending
// in a slash.
func RetainVersionPrefix(repository string, number, version int) string {
	return fmt.Sprintf("%s%s/%d/%d/", RetainPrefix, escapeRepository(strings.ToLower(repository)), number, version)
}

func escapeRepository(repository string) string {
	var b strings.Builder
	for i := 0; i < len(repository); i++ {
		c := repository[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

func unescapeRepository(component string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(component); i++ {
		if component[i] != '%' {
			b.WriteByte(component[i])
			continue
		}
		if i+2 >= len(component) {
			return "", false
		}
		v, err := strconv.ParseUint(component[i+1:i+3], 16, 8)
		if err != nil {
			return "", false
		}
		b.WriteByte(byte(v))
		i += 2
	}
	return b.String(), true
}

// ParseRetainRef reads a retained ref's name. Only a name RetainedName.Ref
// writes exactly parses; any other, under RetainPrefix or not, does not.
func ParseRetainRef(name string) (RetainedName, bool) {
	rest, ok := strings.CutPrefix(name, RetainPrefix)
	if !ok {
		return RetainedName{}, false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 4 || !slices.Contains(RetainRoles, parts[3]) {
		return RetainedName{}, false
	}
	repository, ok := unescapeRepository(parts[0])
	number, numberErr := strconv.Atoi(parts[1])
	version, versionErr := strconv.Atoi(parts[2])
	if !ok || repository == "" || numberErr != nil || versionErr != nil || number <= 0 || version <= 0 {
		return RetainedName{}, false
	}
	parsed := RetainedName{Repository: repository, Number: number, Version: version, Role: parts[3]}
	if parsed.Ref() != name {
		return RetainedName{}, false
	}
	return parsed, true
}

// RefUpdate is one change of a retained ref. An empty Old creates the ref,
// failing if it exists; an empty New deletes it; both set move it, and the
// change fails unless the ref still names Old.
type RefUpdate struct {
	Name, Old, New string
}

// refConfig are the settings of a command on the clone's retained refs: no
// hooks (reference-transaction included), no reflog, no file-system monitor
// and no automatic maintenance.
func (r *Repo) refConfig() []string {
	return []string{"core.hooksPath=" + filepath.Join(r.mirror, "hooks"), "core.logAllRefUpdates=false",
		"core.fsmonitor=false", "gc.auto=0", "maintenance.auto=false"}
}

// refEnv is the environment of a command on the clone's retained refs: the
// private repository's (no GIT_ variable of the caller's, no system or global
// configuration, no replace refs, no lazy fetch, an empty home), with GIT_DIR
// the clone's common directory, whose own configuration git still reads.
func (r *Repo) refEnv() []string {
	env := mirrorEnv(r.mirror)
	for i, kv := range env {
		if strings.HasPrefix(kv, "GIT_DIR=") {
			env[i] = "GIT_DIR=" + r.common
		}
	}
	return env
}

// RetainedRefs lists the clone's refs under prefix, which must be within
// RetainPrefix, by name, with the commit each names.
func (r *Repo) RetainedRefs(ctx context.Context, prefix string) (map[string]string, error) {
	if !strings.HasPrefix(prefix, RetainPrefix) || !strings.HasSuffix(prefix, "/") || !validRefName(strings.TrimSuffix(prefix, "/")) {
		return nil, ErrInvalidInput
	}
	done, err := r.begin()
	if err != nil {
		return nil, err
	}
	defer done()
	out, err := run(ctx, runSpec{dir: r.dir, env: r.refEnv(), limit: maxRetainListBytes, timeout: r.opts.CommandTimeout, config: r.refConfig()},
		"for-each-ref", "--format=%(objectname) %(refname)", "--end-of-options", prefix)
	if err != nil {
		return nil, err
	}
	refs := map[string]string{}
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		oid, name, ok := strings.Cut(line, " ")
		if !ok || !validOID(oid) || !strings.HasPrefix(name, prefix) {
			return nil, errors.New("listing retained refs: unexpected for-each-ref output")
		}
		refs[name] = oid
	}
	return refs, nil
}

// HasCommits reports which of oids are commits the clone holds. Nothing is
// fetched: a commit a partial clone lacks is reported missing.
func (r *Repo) HasCommits(ctx context.Context, oids []string) (map[string]bool, error) {
	var input bytes.Buffer
	asked := map[string]bool{}
	for _, oid := range oids {
		if validOID(oid) && !asked[oid] {
			asked[oid] = true
			input.WriteString(oid + "\n")
		}
	}
	present := map[string]bool{}
	if len(asked) == 0 {
		return present, nil
	}
	done, err := r.begin()
	if err != nil {
		return nil, err
	}
	defer done()
	out, err := run(ctx, runSpec{dir: r.mirror, env: mirrorEnv(r.mirror), limit: int64(len(asked)) * 128, timeout: r.opts.CommandTimeout, stdin: input.Bytes()},
		"cat-file", "--batch-check=%(objectname) %(objecttype)")
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		oid, kind, _ := strings.Cut(line, " ")
		if asked[oid] && kind == "commit" {
			present[oid] = true
		}
	}
	return present, nil
}

// UpdateRetained applies updates to the clone's retained refs as one
// transaction: all of them, or none when any fails. Every name must be one
// ParseRetainRef reads; git runs with no hooks and none of the user's global
// configuration.
func (r *Repo) UpdateRetained(ctx context.Context, updates []RefUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	var input bytes.Buffer
	for _, u := range updates {
		if _, ok := ParseRetainRef(u.Name); !ok || (u.Old == "" && u.New == "") ||
			(u.Old != "" && !validOID(u.Old)) || (u.New != "" && !validOID(u.New)) {
			return ErrInvalidInput
		}
		switch {
		case u.Old == "":
			fmt.Fprintf(&input, "create %s %s\n", u.Name, u.New)
		case u.New == "":
			fmt.Fprintf(&input, "delete %s %s\n", u.Name, u.Old)
		default:
			fmt.Fprintf(&input, "update %s %s %s\n", u.Name, u.New, u.Old)
		}
	}
	done, err := r.begin()
	if err != nil {
		return err
	}
	defer done()
	_, err = run(ctx, runSpec{dir: r.dir, env: r.refEnv(), timeout: r.opts.CommandTimeout, config: r.refConfig(), stdin: input.Bytes()},
		"update-ref", "--stdin")
	return err
}
