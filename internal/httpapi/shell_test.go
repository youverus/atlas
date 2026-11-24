package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"pi-go/internal/wire"
)

// focusStub records whether the shell was asked to raise its window.
type focusStub struct {
	calls atomic.Int32
}

func (f *focusStub) FocusMainWindow() {
	f.calls.Add(1)
}

// petStub answers visibility from a flag and records every set.
type petStub struct {
	visible atomic.Bool
	sets    atomic.Int32
}

func (p *petStub) SetPet(visible bool) {
	p.sets.Add(1)
	p.visible.Store(visible)
}

func (p *petStub) PetVisible() bool {
	return p.visible.Load()
}

func shellServer(t *testing.T, focus FocusShell, pet PetController) *httptest.Server {
	t.Helper()
	server := New(Options{
		Turn:       func(context.Context, string, func(wire.AgentEvent) error) (Receipt, error) { return Receipt{}, nil },
		FocusShell: focus,
		Pet:        pet,
	})
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	return httpServer
}

// Without a shell there is nothing to raise, and the route says so instead of
// pretending the double-click did something.
func TestShellFocusWithoutShellAnswers501(t *testing.T) {
	server := shellServer(t, nil, nil)

	response, err := http.Post(server.URL+"/api/shell/focus", "application/json", nil)
	if err != nil {
		t.Fatalf("post focus: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", response.StatusCode)
	}
}

func TestShellFocusRaisesTheShell(t *testing.T) {
	focus := &focusStub{}
	server := shellServer(t, focus, nil)

	response, err := http.Post(server.URL+"/api/shell/focus", "application/json", nil)
	if err != nil {
		t.Fatalf("post focus: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", response.StatusCode)
	}
	if focus.calls.Load() != 1 {
		t.Fatalf("FocusMainWindow calls = %d, want 1", focus.calls.Load())
	}
}

// The pet routes answer 501 without a shell, so the settings page hides its
// switch instead of drawing a control that cannot do anything.
func TestShellPetWithoutShellAnswers501(t *testing.T) {
	server := shellServer(t, nil, nil)

	for _, spec := range []struct{ method, path string }{
		{"GET", "/api/shell/pet"},
		{"POST", "/api/shell/pet"},
	} {
		request, err := http.NewRequest(spec.method, server.URL+spec.path, strings.NewReader(`{"visible":true}`))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("%s %s: %v", spec.method, spec.path, err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNotImplemented {
			t.Fatalf("%s %s status = %d, want 501", spec.method, spec.path, response.StatusCode)
		}
	}
}

func TestShellPetSetAppliesTheRequestedState(t *testing.T) {
	pet := &petStub{}
	server := shellServer(t, nil, pet)

	response, err := http.Post(server.URL+"/api/shell/pet", "application/json", strings.NewReader(`{"visible":true}`))
	if err != nil {
		t.Fatalf("post pet: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", response.StatusCode)
	}
	if !pet.PetVisible() || pet.sets.Load() != 1 {
		t.Fatalf("pet = %v after %d sets, want true after 1", pet.PetVisible(), pet.sets.Load())
	}
}
