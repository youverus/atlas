package main

import (
	"github.com/wailsapp/wails/v3/pkg/application"
)

// petWindowName names the companion window so the tray toggle can find and
// close it again later.
const petWindowName = "pet"

// togglePetWindow shows the desktop pet when it is absent and closes it when
// it is present. The pet is a frameless, transparent, always-on-top window
// pointing at the same loopback server as the main window: the page at
// /pet.html animates the whale-girl by the sessions' running state and, on a
// double-click, posts /api/shell/focus to bring this app back to the front.
func togglePetWindow(app *application.App, petURL string) {
	if existing, ok := app.Window.GetByName(petWindowName); ok {
		existing.Close()
		return
	}

	options := application.WebviewWindowOptions{
		Name:          petWindowName,
		Title:         "Atlas Pet",
		Width:         190,
		Height:        230,
		Frameless:     true,
		DisableResize: true,
		AlwaysOnTop:   true,
		URL:           petURL,
		Mac: application.MacWindow{
			Backdrop:      application.MacBackdropTransparent,
			DisableShadow: true,
			// A non-activating panel: clicking the pet must not steal focus
			// from whatever the reader is typing in — the whole point of a
			// desktop pet is to watch without touching.
			WindowClass:      application.MacWindowClassPanel,
			PanelPreferences: application.MacPanelPreferences{NonActivating: true, FloatingPanel: true},
			WindowLevel:      application.MacWindowLevelFloating,
			CollectionBehavior: application.MacWindowCollectionBehaviorCanJoinAllSpaces |
				application.MacWindowCollectionBehaviorFullScreenAuxiliary,
		},
	}
	// Screens are cached once the run loop starts, and the toggle can only be
	// clicked afterwards, so the work area is available here. With Screen set,
	// WindowXY coordinates are relative to that screen's work-area origin,
	// which keeps the pet in the corner of the primary display on any setup.
	// Without it the window keeps its centered default and can be dragged
	// anywhere.
	if screen := app.Screen.GetPrimary(); screen != nil {
		options.Screen = screen
		options.InitialPosition = application.WindowXY
		options.X = screen.WorkArea.Width - options.Width - 24
		options.Y = screen.WorkArea.Height - options.Height - 8
	}
	app.Window.NewWithOptions(options)
}

// shellBridge carries the two shell capabilities the pages reach over HTTP:
// the pet's double-click asks for the main window (httpapi.FocusShell) and the
// settings switch drives the pet window itself (httpapi.PetController). Like
// folderPicker, the struct reaches the server before the application exists
// and is filled in right after; nothing can click before a window is up, so
// the deferred assignment always wins the race.
type shellBridge struct {
	app    *application.App
	win    *application.WebviewWindow
	petURL string
}

func (f *shellBridge) FocusMainWindow() {
	if f.app == nil || f.win == nil {
		return
	}
	showMainWindow(f.app, f.win)
}

func (f *shellBridge) PetVisible() bool {
	if f.app == nil {
		return false
	}
	_, ok := f.app.Window.GetByName(petWindowName)
	return ok
}

// SetPet shows or closes the pet window to match visible; matching the
// current state is a no-op, so a stale page and the tray menu — two controls
// over one window — cannot fight each other.
func (f *shellBridge) SetPet(visible bool) {
	if f.app == nil || f.PetVisible() == visible {
		return
	}
	togglePetWindow(f.app, f.petURL)
}
