package githooks

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Clone is a registered clone as the service needs it.
type Clone struct {
	ID, Path, CommonDir string
}

// Service keeps the dispatchers installed in the registered clones: for a
// clone as it is registered, and for every available clone at start and daily
// after, which repairs dispatchers an update outdated or a tool replaced. A
// clone that is no longer registered gets its own hooks back.
type Service struct {
	// Clones lists the available registered clones.
	Clones func(ctx context.Context) ([]Clone, error)
	// Registered lists every registered clone, available or not; Removed uses
	// it to leave hooks that another clone of the same repository shares.
	Registered func(ctx context.Context) ([]Clone, error)
	// Ensure and Restore act on one clone; nil are EnsureWith and Restore.
	Ensure  func(ctx context.Context, root string) (Report, error)
	Restore func(ctx context.Context, root string) (Report, error)
	// Options are passed to the default Ensure and Restore.
	Options Options
	Log     *slog.Logger
	// Every is the period of the sweep; zero is a day.
	Every time.Duration

	mu  sync.Mutex
	run context.Context
	// ensuring holds the work on a clone's hooks to one caller at a time, so a
	// registration and the sweep do not both chain the same hook.
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
	clones, err := s.Clones(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.logger().Warn("Git hooks not ensured: clones not listed", "error", err)
		}
		return
	}
	for _, c := range clones {
		if ctx.Err() != nil {
			return
		}
		s.one(ctx, c, true)
	}
}

// Added ensures a clone just registered, in the background; it never blocks
// the registration or fails it.
func (s *Service) Added(c Clone) { s.background(c, true) }

// Removed gives a clone no longer registered its own hooks back, in the
// background, unless another registered clone shares its hooks.
func (s *Service) Removed(c Clone) { s.background(c, false) }

func (s *Service) background(c Clone, ensure bool) {
	s.mu.Lock()
	ctx := s.run
	s.mu.Unlock()
	if ctx == nil || ctx.Err() != nil {
		return
	}
	go s.one(ctx, c, ensure)
}

func (s *Service) one(ctx context.Context, c Clone, ensure bool) {
	s.ensuring.Lock()
	defer s.ensuring.Unlock()
	log := s.logger().With("clone", c.ID)
	var report Report
	var err error
	if ensure {
		fn := s.Ensure
		if fn == nil {
			fn = func(ctx context.Context, root string) (Report, error) { return EnsureWith(ctx, root, s.Options) }
		}
		report, err = fn(ctx, c.Path)
	} else {
		if s.sharesHooks(ctx, c) {
			return
		}
		fn := s.Restore
		if fn == nil {
			fn = func(ctx context.Context, root string) (Report, error) { return Restore(ctx, root, s.Options) }
		}
		report, err = fn(ctx, c.Path)
	}
	// Results are reported alongside an error for the hooks that did work.
	if err != nil && ctx.Err() == nil {
		log.Warn("Git hooks not fully handled", "error", err)
	}
	if report.Skipped != "" {
		if ensure {
			log.Info("Git hooks not installed in a clone", "reason", report.Skipped)
		}
		return
	}
	for _, h := range report.Hooks {
		switch h.Outcome {
		case Unclaimed:
			log.Warn("Git hook left as it is", "hook", h.Hook, "reason", h.Reason)
		case Installed, Repaired, Chained, Restored, Removed:
			log.Info("Git hook handled", "hook", h.Hook, "outcome", string(h.Outcome))
		}
	}
}

// sharesHooks reports whether another registered clone has c's hooks directory,
// which linked worktrees of one repository share.
func (s *Service) sharesHooks(ctx context.Context, c Clone) bool {
	if s.Registered == nil || c.CommonDir == "" {
		return false
	}
	clones, err := s.Registered(ctx)
	if err != nil {
		// Unknown: leave the hooks rather than take them from a clone in use.
		return true
	}
	for _, other := range clones {
		if other.ID != c.ID && other.CommonDir == c.CommonDir {
			return true
		}
	}
	return false
}

func (s *Service) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}
