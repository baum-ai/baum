package githooks

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Service keeps the dispatchers installed in the registered clones: for a
// clone as it is registered, and for every available clone at start and daily
// after, which repairs dispatchers an update outdated or a tool replaced.
type Service struct {
	// Paths lists the working trees of the available registered clones.
	Paths func(ctx context.Context) ([]string, error)
	// Ensure installs the dispatchers into one clone; nil is Ensure.
	Ensure func(ctx context.Context, root string) (Report, error)
	Log    *slog.Logger
	// Every is the period of the sweep; zero is a day.
	Every time.Duration

	mu  sync.Mutex
	run context.Context
	// one holds a clone's ensuring to one caller at a time, so a registration
	// and the sweep do not both chain the same hook.
	ensuring sync.Mutex
}

// Run ensures every clone at once and then every period, until ctx ends.
func (s *Service) Run(ctx context.Context) {
	s.mu.Lock()
	s.run = ctx
	s.mu.Unlock()
	every := s.Every
	if every <= 0 {
		every = 24 * time.Hour
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		s.All(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// All ensures every available clone.
func (s *Service) All(ctx context.Context) {
	paths, err := s.Paths(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.logger().Warn("Git hooks not ensured: clones not listed", "error", err)
		}
		return
	}
	for _, path := range paths {
		if ctx.Err() != nil {
			return
		}
		s.one(ctx, path)
	}
}

// Added ensures a clone just registered, in the background; it never blocks
// the registration or fails it.
func (s *Service) Added(path string) {
	s.mu.Lock()
	ctx := s.run
	s.mu.Unlock()
	if ctx == nil || ctx.Err() != nil {
		return
	}
	go s.one(ctx, path)
}

func (s *Service) one(ctx context.Context, path string) {
	s.ensuring.Lock()
	defer s.ensuring.Unlock()
	ensure := s.Ensure
	if ensure == nil {
		ensure = Ensure
	}
	report, err := ensure(ctx, path)
	log := s.logger()
	switch {
	case err != nil:
		if ctx.Err() == nil {
			log.Warn("Git hooks not ensured", "error", err)
		}
		return
	case report.Skipped != "":
		log.Info("Git hooks not installed in a clone", "reason", report.Skipped)
		return
	}
	for _, h := range report.Hooks {
		switch h.Outcome {
		case Unclaimed:
			log.Warn("Git hook left as it is", "hook", h.Hook, "reason", h.Reason)
		case Installed, Repaired, Chained:
			log.Info("Git hook ensured", "hook", h.Hook, "outcome", string(h.Outcome))
		}
	}
}

func (s *Service) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}
