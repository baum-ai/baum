package credentialstore

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/zalando/go-keyring"
)

// nativeStore keeps the selected account and tokens together in one OS keyring
// item, namespaced by GitHub host. No plaintext file fallback is permitted.
type nativeStore struct{ service string }

var ErrStore = errors.New("operating system credential store unavailable")
var ErrNotConnected = errors.New("credential not found")

const account = "desktop-account"

func (n nativeStore) Load(context.Context) (json.RawMessage, error) {
	s, err := keyring.Get(n.service, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, ErrNotConnected
	}
	if err != nil {
		return nil, ErrStore
	}
	if !json.Valid([]byte(s)) {
		return nil, ErrStore
	}
	return json.RawMessage(s), nil
}
func (n nativeStore) Save(_ context.Context, c json.RawMessage) error {
	if keyring.Set(n.service, account, string(c)) != nil {
		return ErrStore
	}
	return nil
}
func (n nativeStore) Delete(context.Context) error {
	err := keyring.Delete(n.service, account)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return ErrStore
	}
	return nil
}
