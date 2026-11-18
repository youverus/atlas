package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// permissionHost answers only the two posture questions; every other method of
// the interface is left to the embedded nil.
//
// The rows are literal here on purpose: this test is about the transport —
// whether the sheet can read the mode, switch it, and be told why a switch was
// refused — and the words themselves are tested beside the modes they name.
type permissionHost struct {
	Host
	mode string
}

func (h *permissionHost) Permission() (PermissionState, error) {
	return PermissionState{
		Mode: h.mode,
		Modes: []PermissionMode{
			{Value: "default", Label: "Ask"},
			{Value: "auto", Label: "Auto"},
			{Value: "bypassPermissions", Label: "Full access"},
			{Value: "off", Label: "Off"},
		},
	}, nil
}

func (h *permissionHost) SetPermission(mode string) error {
	switch mode {
	case "default", "auto", "bypassPermissions", "off":
		h.mode = mode
		return nil
	}
	return fmt.Errorf("permission: %q is not one of default, auto, bypassPermissions, off", mode)
}

// A window that opens the sheet asks what the gate is doing right now, and gets
// the whole menu with it: four rows and no current one is a control that cannot
// draw its own selection.
func TestServePermissionReportsModeAndMenu(t *testing.T) {
	host := &permissionHost{mode: "default"}
	server := projectServer(t, host, nil)

	response, err := http.Get(server.URL + "/api/permission")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	var state PermissionState
	if err := json.NewDecoder(response.Body).Decode(&state); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if state.Mode != "default" {
		t.Fatalf("mode = %q, want default", state.Mode)
	}
	if len(state.Modes) != 4 {
		t.Fatalf("modes = %d rows, want the four postures", len(state.Modes))
	}
	if state.Modes[2].Value != "bypassPermissions" || state.Modes[2].Label != "Full access" {
		t.Fatalf("row = %+v, want the config value with the reader-facing label", state.Modes[2])
	}
}

// The switch answers with the state it produced. A sheet that had to ask again
// would be a second request that can disagree with the first.
func TestServePermissionSetAnswersWithTheNewMode(t *testing.T) {
	host := &permissionHost{mode: "default"}
	server := projectServer(t, host, nil)

	response := postJSON(t, server.URL+"/api/permission", `{"mode":"bypassPermissions"}`)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	var state PermissionState
	if err := json.NewDecoder(response.Body).Decode(&state); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if state.Mode != "bypassPermissions" {
		t.Fatalf("mode = %q, want the switch to have taken effect", state.Mode)
	}
	if host.mode != "bypassPermissions" {
		t.Fatalf("host mode = %q, want the kernel to have been asked", host.mode)
	}
}

// A name the kernel does not know is refused rather than stored: a typo that
// silently became "ask" would look like the switch did nothing.
func TestServePermissionSetRefusesAnUnknownMode(t *testing.T) {
	server := projectServer(t, &permissionHost{mode: "default"}, nil)

	response := postJSON(t, server.URL+"/api/permission", `{"mode":"paranoid"}`)
	defer response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", response.StatusCode)
	}
}

func TestServePermissionSetRefusesAnEmptyMode(t *testing.T) {
	server := projectServer(t, &permissionHost{mode: "default"}, nil)

	response := postJSON(t, server.URL+"/api/permission", `{"mode":""}`)
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.StatusCode)
	}
}
