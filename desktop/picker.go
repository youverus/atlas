package main

import (
	"errors"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// folderPicker is the native chooser behind POST /api/projects/pick.
//
// It holds the application rather than taking one because of the order main
// builds things in: the server has to exist before the window, and the window
// before there is anything to open a dialog with. The field is written once,
// before app.Run, and read only when a page asks for a folder.
type folderPicker struct {
	app *application.App
}

// PickFolder opens the system's folder chooser and waits for it.
//
// Cancelling is not an error: it returns an empty path, and the page has
// nothing to add.
func (p *folderPicker) PickFolder() (string, error) {
	if p.app == nil {
		return "", errors.New("atlas-desktop: the window is not up yet")
	}
	return p.app.Dialog.OpenFileWithOptions(&application.OpenFileDialogOptions{
		// A project is a directory, so files are not offered: a row that
		// cannot become a project is a row that wastes a click.
		CanChooseDirectories: true,
		CanChooseFiles:       false,
		CanCreateDirectories: true,
		Title:                "添加项目",
		ButtonText:           "添加",
	}).PromptForSingleSelection()
}
