//go:build darwin

package main

/*
#cgo CFLAGS: -mmacosx-version-min=10.13 -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

static void unhideAppIfHidden(void) {
	if ([NSApp isHidden]) {
		[NSApp unhide:nil];
	}
}
*/
import "C"

import "github.com/wailsapp/wails/v3/pkg/application"

// unhideAppIfHidden dispatches [NSApp unhide:nil] onto the main thread and only
// calls it when hidden. Calling Wails's App.Show directly from a worker
// goroutine triggers a SIGTRAP in AppKit, and calling unhide when not hidden
// leaves the reopened window off the active Space.
func unhideAppIfHidden(_ *application.App) {
	application.InvokeSync(func() {
		C.unhideAppIfHidden()
	})
}
