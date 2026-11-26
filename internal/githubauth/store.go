package githubauth

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/baum-ai/baum/internal/credentialstore"
)

type ProcessStore struct{}

var githubStore = credentialstore.ProcessStore{Service: "baum.github.com"}

func (ProcessStore) Load(ctx context.Context) (Credential, error) {
	raw, err := githubStore.Load(ctx)
	if errors.Is(err, credentialstore.ErrNotConnected) {
		return Credential{}, ErrNotConnected
	}
	var c Credential
	if err != nil || json.Unmarshal(raw, &c) != nil || c.AccessToken == "" || c.Login == "" || c.ID <= 0 || c.ExpiresAt < 0 || (c.ExpiresAt > 0 && (c.RefreshToken == "" || c.RefreshExpiresAt <= 0)) {
		return Credential{}, ErrStore
	}
	return c, nil
}
func (ProcessStore) Save(ctx context.Context, c Credential) error {
	if c.AccessToken == "" || c.Login == "" || c.ID <= 0 {
		return ErrStore
	}
	raw, err := json.Marshal(c)
	if err != nil || githubStore.Save(ctx, raw) != nil {
		return ErrStore
	}
	return nil
}
func (ProcessStore) Delete(ctx context.Context) error {
	if githubStore.Delete(ctx) != nil {
		return ErrStore
	}
	return nil
}
