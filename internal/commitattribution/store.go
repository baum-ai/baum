// Package commitattribution retains session-owned Git hook observations beyond
// the lifetime of the live session tracker. Pushes are context; only a
// post-commit observation establishes commit creation.
package commitattribution

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/appdb"
	"github.com/baum-ai/baum/sessions/vendors"
)

const bucket = "attribution/commits"
const Commit = "commit"
const Push = "push"

var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// Observation records the session identified by a Git hook's process ancestry.
// Repository includes the GitHub host for enterprise repositories. No local
// paths or conversation text are retained.
type Observation struct {
	Repository string    `json:"repository"`
	CommitSHA  string    `json:"commit"`
	Agent      string    `json:"agent"`
	SessionID  string    `json:"session"`
	Kind       string    `json:"kind"`
	EventID    string    `json:"event"`
	At         time.Time `json:"at"`
}

// Store uses the shared application database. The empty Path selects its
// default location. Reads never create a database, and records have no TTL.
type Store struct{ Path string }

// Vendor translates the live hook agent spelling to the session store spelling.
func Vendor(agent string) string {
	if agent == "claude-code" {
		return vendors.ClaudeCode.String()
	}
	return agent
}

func normalize(o Observation) (Observation, error) {
	o.Repository = strings.ToLower(o.Repository)
	o.CommitSHA = strings.ToLower(o.CommitSHA)
	o.Agent = Vendor(o.Agent)
	if o.Repository == "" || !strings.Contains(o.Repository, "/") || !shaPattern.MatchString(o.CommitSHA) || strings.Trim(o.CommitSHA, "0") == "" ||
		!vendors.Slug(o.Agent).Valid() || o.SessionID == "" || len(o.SessionID) > 128 || o.EventID == "" || o.At.IsZero() || (o.Kind != Commit && o.Kind != Push) {
		return o, errors.New("invalid session commit observation")
	}
	o.At = o.At.UTC()
	return o, nil
}

// Record merges observations idempotently under SQLite's write transaction.
// A failed write leaves the prior records available for retry.
func (s *Store) Record(observations []Observation) error {
	grouped := map[string][]Observation{}
	for _, o := range observations {
		normalized, err := normalize(o)
		if err != nil {
			return err
		}
		key := normalized.Repository + "/" + normalized.CommitSHA
		grouped[key] = append(grouped[key], normalized)
	}
	keys := make([]string, 0, len(grouped))
	for key := range grouped {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if err := appdb.Update(s.Path, bucket, key, func(raw []byte) ([]byte, error) {
			var saved []Observation
			if raw != nil {
				if err := json.Unmarshal(raw, &saved); err != nil {
					return nil, fmt.Errorf("reading commit observations: %w", err)
				}
			}
			for _, o := range grouped[key] {
				if !slices.ContainsFunc(saved, func(previous Observation) bool {
					return previous.Agent == o.Agent && previous.SessionID == o.SessionID && previous.Kind == o.Kind && previous.EventID == o.EventID
				}) {
					saved = append(saved, o)
				}
			}
			return json.Marshal(saved)
		}); err != nil {
			return err
		}
	}
	return nil
}

// Lookup reads observations of exactly these commits, scoped to repositories
// named by the clone's remotes. Missing application state is an empty result.
func (s *Store) Lookup(ctx context.Context, repositories, commits []string) ([]Observation, error) {
	if len(repositories) == 0 || len(commits) == 0 {
		return nil, nil
	}
	db, release, err := appdb.Acquire(s.Path, true)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer release()
	var out []Observation
	for _, repository := range repositories {
		for _, sha := range commits {
			var raw []byte
			err := db.QueryRowContext(ctx, `SELECT value FROM app_documents WHERE bucket=? AND key=?`, bucket, strings.ToLower(repository)+"/"+strings.ToLower(sha)).Scan(&raw)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return nil, err
			}
			var observations []Observation
			if err := json.Unmarshal(raw, &observations); err != nil {
				return nil, err
			}
			for _, o := range observations {
				o, err = normalize(o)
				if err != nil {
					return nil, err
				}
				if o.Repository != strings.ToLower(repository) || o.CommitSHA != strings.ToLower(sha) {
					return nil, errors.New("commit observation does not match its storage key")
				}
				out = append(out, o)
			}
		}
	}
	return out, nil
}
