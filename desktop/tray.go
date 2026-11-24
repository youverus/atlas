package main

import (
	_ "embed"
	"sync"
	"sync/atomic"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed build/appicon.png
var appIcon []byte

var showMu sync.Mutex

// configureBackgroundShell hides the window on close instead of terminating the
// process, adds a macOS menu-bar tray icon with a dropdown to restore or hide
// the window, toggles the desktop pet, and reserves real termination for
// explicit quit actions (Cmd+Q, Dock Quit, or the tray menu).
func configureBackgroundShell(app *application.App, win *application.WebviewWindow, petURL string, quitting *atomic.Bool) {
	win.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		if quitting.Load() {
			return
		}
		win.Hide()
		event.Cancel()
	})

	app.Event.RegisterApplicationEventHook(events.Mac.ApplicationShouldHandleReopen, func(event *application.ApplicationEvent) {
		showMainWindow(app, win)
		event.Cancel()
	})

	tray := app.SystemTray.New()
	tray.SetIcon(appIcon)
	tray.SetIconPosition(application.NSImageOnly)

	menu := app.NewMenu()
	menu.Add("显示主窗口").OnClick(func(_ *application.Context) {
		showMainWindow(app, win)
	})
	menu.Add("隐藏到后台").OnClick(func(_ *application.Context) {
		win.Hide()
	})
	menu.Add("显示/隐藏桌面宠物").OnClick(func(_ *application.Context) {
		togglePetWindow(app, petURL)
	})
	menu.AddSeparator()
	info := menu.Add("Atlas v" + version)
	info.SetEnabled(false)
	menu.AddSeparator()
	menu.Add("退出 Atlas").
		SetAccelerator("CmdOrCtrl+q").
		OnClick(func(_ *application.Context) {
			quitting.Store(true)
			app.Quit()
		})
	tray.SetMenu(menu)
}

func showMainWindow(app *application.App, win *application.WebviewWindow) {
	showMu.Lock()
	defer showMu.Unlock()

	unhideAppIfHidden(app)
	if win.IsMinimised() {
		win.UnMinimise()
	}
	win.Show()
	win.Focus()
}
