package githubauth

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/zalando/go-keyring"
)

// nativeStore keeps the selected account and tokens together in one OS keyring
// item, namespaced by GitHub host. No plaintext file fallback is permitted.
type nativeStore struct{}

const service = "baum.github.com"
const account = "desktop-account"

func (nativeStore) Load(context.Context) (Credential, error) {
	s, err := keyring.Get(service, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return Credential{}, ErrNotConnected
	}
	if err != nil {
		return Credential{}, ErrStore
	}
	var c Credential
	if json.Unmarshal([]byte(s), &c) != nil || c.AccessToken == "" || c.Login == "" || c.ID <= 0 || c.ExpiresAt <= 0 || c.RefreshToken == "" || c.RefreshExpiresAt <= 0 {
		return Credential{}, ErrStore
	}
	return c, nil
}
func (nativeStore) Save(_ context.Context, c Credential) error {
	b, err := json.Marshal(c)
	if err != nil {
		return ErrStore
	}
	if keyring.Set(service, account, string(b)) != nil {
		return ErrStore
	}
	return nil
}
func (nativeStore) Delete(context.Context) error {
	err := keyring.Delete(service, account)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return ErrStore
	}
	return nil
}
