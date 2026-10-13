// Package names knows the display names of the satellites, asked from Hannah Core. The forwarded
// syslog carries them, so a receiver such as Loki can label and alert by "Test01" instead of a MAC.
package names

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const (
	// retryEvery is the wait after a failed call, in particular before the first success: Core may
	// still be starting.
	retryEvery = 30 * time.Second
	// refreshEvery is how often the names are asked again once Core answered, so a renamed
	// satellite shows up without a restart of the collector.
	refreshEvery = 5 * time.Minute
)

// Lister asks Hannah Core for the display names: device ID to name, only satellites that have one.
type Lister func(ctx context.Context) (map[string]string, error)

// Registry holds the names. Safe for concurrent use.
type Registry struct {
	mu   sync.RWMutex
	byID map[string]string
}

// New creates an empty registry: until Run got an answer, every ID is its own name.
func New() *Registry {
	return &Registry{byID: map[string]string{}}
}

// Name is the display name of id, the id itself when it has none (never empty for a non-empty id).
func (r *Registry) Name(id string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if name := r.byID[id]; name != "" {
		return name
	}
	return id
}

func (r *Registry) set(byID map[string]string) {
	r.mu.Lock()
	r.byID = byID
	r.mu.Unlock()
}

// Run asks list for the names until ctx ends: at once, then every 30 s while it fails and every
// five minutes while it works. A failed call keeps the names of the last good one. Blocks.
func (r *Registry) Run(ctx context.Context, list Lister) {
	r.run(ctx, list, retryEvery, refreshEvery)
}

func (r *Registry) run(ctx context.Context, list Lister, retry, refresh time.Duration) {
	failing := false // the first failure is worth a line, a retry every 30 s is not
	loaded := false
	for {
		wait := refresh
		byID, err := list(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			wait = retry
			if !failing {
				slog.Warn("satellite names not available from Hannah, using the IDs for now", "err", err)
				failing = true
			}
		} else {
			r.set(byID)
			if failing || !loaded {
				slog.Info("satellite names loaded", "count", len(byID))
			}
			failing, loaded = false, true
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}
