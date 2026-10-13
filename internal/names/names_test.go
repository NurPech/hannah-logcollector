package names

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNameIsTheDisplayNameOrElseTheID(t *testing.T) {
	r := New()
	r.set(map[string]string{"9070690de440": "Test02", "empty": ""})

	assert.Equal(t, "Test02", r.Name("9070690de440"))
	assert.Equal(t, "9070690de424", r.Name("9070690de424"), "unknown: the ID")
	assert.Equal(t, "empty", r.Name("empty"), "an empty name is no name")
}

func TestNameBeforeAnythingWasLoadedIsTheID(t *testing.T) {
	assert.Equal(t, "e072a1d01ac0", New().Name("e072a1d01ac0"))
}

// script answers one call per entry, the last one for every further call.
type script struct {
	mu      sync.Mutex
	answers []func() (map[string]string, error)
	calls   int
}

func (s *script) list(context.Context) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := min(s.calls, len(s.answers)-1)
	s.calls++
	return s.answers[i]()
}

func (s *script) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func ok(m map[string]string) func() (map[string]string, error) {
	return func() (map[string]string, error) { return m, nil }
}

func fail() (map[string]string, error) { return nil, errors.New("Core down") }

func runFor(t *testing.T, r *Registry, s *script) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.run(ctx, s.list, 10*time.Millisecond, 10*time.Millisecond); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Run did not stop")
		}
	})
}

func TestRunLoadsTheNamesAndKeepsAskingForRenames(t *testing.T) {
	r := New()
	s := &script{answers: []func() (map[string]string, error){
		ok(map[string]string{"9070690de440": "Test02"}),
		ok(map[string]string{"9070690de440": "Test02b"}),
	}}
	runFor(t, r, s)

	require.Eventually(t, func() bool { return r.Name("9070690de440") == "Test02b" }, 2*time.Second, 5*time.Millisecond)
}

func TestRunRetriesAfterAFailureAndKeepsTheLastGoodNames(t *testing.T) {
	r := New()
	s := &script{answers: []func() (map[string]string, error){
		fail,
		ok(map[string]string{"9070690de440": "Test02"}),
		fail,
	}}
	runFor(t, r, s)

	require.Eventually(t, func() bool { return r.Name("9070690de440") == "Test02" }, 2*time.Second, 5*time.Millisecond)
	// The calls after the success fail again, the name stays.
	require.Eventually(t, func() bool { return s.count() >= 5 }, 2*time.Second, 5*time.Millisecond)
	assert.Equal(t, "Test02", r.Name("9070690de440"))
}

func TestRunStopsWithTheContext(t *testing.T) {
	r := New()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(ctx, func(context.Context) (map[string]string, error) { return nil, errors.New("down") })
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}
