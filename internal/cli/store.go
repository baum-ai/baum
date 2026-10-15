package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/store"
)

func storedVersionFlags(cmd *cobra.Command) {
	cmd.Flags().String("store", "", "local version store directory")
	cmd.Flags().String("repo", "", "GitHub repository as owner/name (default: origin remote)")
	cmd.Flags().Int("version", 0, "stored version number (default: latest; requires --pr)")
}
func checkStoredFlags(cmd *cobra.Command) error {
	if !cmd.Flags().Changed("pr") {
		for _, name := range []string{"version", "repo", "store"} {
			if cmd.Flags().Changed(name) {
				return fmt.Errorf("--%s requires --pr", name)
			}
		}
	}
	return nil
}
func loadStoredVersion(cmd *cobra.Command) (*store.Store, store.Key, store.Version, error) {
	flags := cmd.Flags()
	number, _ := flags.GetInt("pr")
	n, _ := flags.GetInt("version")
	if number <= 0 || n < 0 {
		return nil, store.Key{}, store.Version{}, fmt.Errorf("--pr must be positive and --version must not be negative")
	}
	clone, err := gitlocal.Open(cmd.Context(), ".", gitlocal.Options{})
	if err != nil {
		return nil, store.Key{}, store.Version{}, err
	}
	defer func() { _ = clone.Close() }()
	name, _ := flags.GetString("repo")
	var repository github.Repository
	if name != "" {
		repository, err = github.ParseRepository(name)
	} else {
		var remote string
		remote, err = clone.RemoteFetchURL(cmd.Context(), gitlocal.DefaultRemote)
		if err == nil {
			repository, err = github.RepositoryFromRemote(remote)
		}
	}
	if err != nil {
		return nil, store.Key{}, store.Version{}, err
	}
	override, _ := flags.GetString("store")
	versions, err := store.Open(clone.CommonDir(), override, false)
	key := store.Key{Repository: repository.String(), Number: number}
	if err != nil {
		return nil, key, store.Version{}, err
	}
	var v store.Version
	if n == 0 {
		v, err = versions.Latest(cmd.Context(), key)
	} else {
		v, err = versions.Get(cmd.Context(), key, n)
	}
	return versions, key, v, err
}
