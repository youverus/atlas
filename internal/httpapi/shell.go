package httpapi

import (
	"encoding/json"
	"net/http"
)

// FocusShell raises the surrounding shell's main window. The desktop pet page
// asks for it on a double-click, and only a desktop shell has a window to
// raise — other builds answer 501, the same convention as Picker.
type FocusShell interface {
	// FocusMainWindow brings the shell's primary window to the front.
	FocusMainWindow()
}

// PetController manages the desktop pet companion window. Only a desktop
// shell has one; nil causes /api/shell/pet routes to answer 501, which is the
// signal the settings page uses to hide its pet switch instead of showing a
// control that can do nothing.
type PetController interface {
	// SetPet shows or closes the pet window to match visible.
	SetPet(visible bool)
	// PetVisible reports whether the pet window currently exists.
	PetVisible() bool
}

// serveShellFocus raises the shell's main window: the desktop pet's
// double-click lands here. There is nothing to say back — the call either
// happens, or this build has no shell to ask (501, the page's cue to treat
// the gesture as a no-op).
func (s *Server) serveShellFocus(w http.ResponseWriter, _ *http.Request) {
	if s.focusShell == nil {
		http.Error(w, "no shell to focus in this build", http.StatusNotImplemented)
		return
	}
	s.focusShell.FocusMainWindow()
	w.WriteHeader(http.StatusNoContent)
}

// servePetStatus answers whether the pet window is currently up. The settings
// page reads it to draw its switch and hides the whole field on 501, because
// a browser build has no window to toggle.
func (s *Server) servePetStatus(w http.ResponseWriter, _ *http.Request) {
	if s.pet == nil {
		http.Error(w, "no shell pet in this build", http.StatusNotImplemented)
		return
	}
	_ = json.NewEncoder(w).Encode(petState{Visible: s.pet.PetVisible()})
}

// servePetSet shows or closes the pet window to match the requested state.
// It answers 204 without echoing visibility: closing tears the window down
// asynchronously, so a state read right after the set can still say true.
// GET /api/shell/pet is the query; SetPet is idempotent, so a client that
// keeps its requested value converges with the shell even if the tray menu
// toggled the same window in between.
func (s *Server) servePetSet(w http.ResponseWriter, r *http.Request) {
	if s.pet == nil {
		http.Error(w, "no shell pet in this build", http.StatusNotImplemented)
		return
	}
	var request petState
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&request); err != nil {
		http.Error(w, `body must be {"visible": true|false}`, http.StatusBadRequest)
		return
	}
	s.pet.SetPet(request.Visible)
	w.WriteHeader(http.StatusNoContent)
}

// petState is the one fact either pet route exists to carry.
type petState struct {
	Visible bool `json:"visible"`
}
