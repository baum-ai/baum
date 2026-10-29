package agentrequests

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/baum-ai/baum/internal/daemon"
)

// Methods are the control requests the store answers at once.
func (s *Store) Methods() map[string]daemon.Method {
	return map[string]daemon.Method{MethodTakeMessage: s.take}
}

// Waits are the control requests the store holds (daemon.Options.Waits).
func (s *Store) Waits() map[string]daemon.Method {
	return map[string]daemon.Method{MethodRequest: s.request}
}

func (s *Store) request(ctx context.Context, params json.RawMessage) (any, error) {
	var ask Ask
	if err := json.Unmarshal(params, &ask); err != nil {
		return nil, &daemon.Error{Code: daemon.CodeBadRequest, Message: "the request is not valid JSON"}
	}
	decision, err := s.Ask(ctx, ask)
	if badRequest, ok := errors.AsType[*BadRequestError](err); ok {
		return nil, &daemon.Error{Code: daemon.CodeBadRequest, Message: badRequest.Reason}
	}
	return decision, err
}

func (s *Store) take(_ context.Context, params json.RawMessage) (any, error) {
	var take TakeParams
	if err := json.Unmarshal(params, &take); err != nil {
		return nil, &daemon.Error{Code: daemon.CodeBadRequest, Message: "the request is not valid JSON"}
	}
	message, ok := s.TakeMessage(take.Agent, take.SessionID)
	if !ok {
		return Taken{}, nil
	}
	// A message whose answer could not be written goes back in the queue;
	// one handed over resumes the session's turn.
	return daemon.Delivery{Result: Taken{Text: message.Text}, Written: func(err error) {
		if err != nil {
			s.Requeue(message)
		} else if s.Resumed != nil {
			s.Resumed(message.Agent, message.SessionID, s.now())
			s.notify()
		}
	}}, nil
}
