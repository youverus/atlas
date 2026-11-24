//go:build !darwin

package main

import "github.com/wailsapp/wails/v3/pkg/application"

func unhideAppIfHidden(app *application.App) {
	application.InvokeSync(app.Show)
}
