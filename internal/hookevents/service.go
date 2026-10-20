package hookevents

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/daemon"
)

// DrainInterval is how often a running daemon drains the spool, besides once
// when it starts: hooks spool an event the daemon did not take in time, or
// could not read.
const DrainInterval = 30 * time.Second

// Service is the daemon's side: it serves Method on the control socket,
// drains the spool, and keeps the record in Store.
type Service struct {
	Store   *Store
	spool   *Spool
	log     *slog.Logger
	drainMu sync.Mutex
}

// Receipt answers Method.
type Receipt struct {
	// Recorded is false for an event the daemon had already recorded.
	Recorded bool `json:"recorded"`
}

// Open opens the record of the daemon whose state directory paths name. Only
// the daemon that holds the daemon lock calls it.
func Open(paths daemon.Paths, log *slog.Logger) (*Service, error) {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	store, err := OpenStore(RecordDir(paths), log)
	if err != nil {
		return nil, err
	}
	return &Service{Store: store, spool: &Spool{Dir: SpoolDir(paths), Log: log}, log: log}, nil
}

// Methods are the control requests the service serves.
func (s *Service) Methods() map[string]daemon.Method {
	return map[string]daemon.Method{Method: s.receive}
}

// receive records one event a hook sent. The event's fields this daemon does
// not know are ignored; an event of a newer Version is refused with
// CodeEventVersion, so the hook spools it for a newer daemon.
func (s *Service) receive(_ context.Context, params json.RawMessage) (any, error) {
	var e Event
	if err := json.Unmarshal(params, &e); err != nil {
		return nil, &daemon.Error{Code: daemon.CodeBadRequest, Message: "the hook event is not valid JSON"}
	}
	switch err := e.check(); {
	case errors.Is(err, errNewer):
		return nil, &daemon.Error{Code: CodeEventVersion, Message: err.Error()}
	case err != nil:
		return nil, &daemon.Error{Code: daemon.CodeBadRequest, Message: err.Error()}
	}
	recorded, err := s.Store.Add(e, ViaSocket)
	if err != nil {
		s.log.Error("hook event not recorded", "kind", e.Kind, "name", e.Name, "error", err)
		return nil, err
	}
	return Receipt{Recorded: recorded}, nil
}

// Run drains the spool now and every DrainInterval until ctx ends.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(DrainInterval)
	defer ticker.Stop()
	for {
		s.Drain()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Drain records every spooled event this daemon reads.
func (s *Service) Drain() {
	s.drainMu.Lock()
	defer s.drainMu.Unlock()
	drained := 0
	err := s.spool.Drain(func(e Event) error {
		_, err := s.Store.Add(e, ViaSpool)
		if err == nil {
			drained++
		}
		return err
	})
	if err != nil {
		s.log.Error("hook event spool not drained; retrying later", "error", err)
	}
	if drained > 0 {
		s.log.Info("drained spooled hook events", "count", drained)
	}
}

// Close closes the record.
func (s *Service) Close() error { return s.Store.Close() }
