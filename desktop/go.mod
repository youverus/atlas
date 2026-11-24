// Module pi-go/desktop is the windowed shell. It is a separate module on
// purpose: it needs cgo and the platform's WebView, while the CLI is
// cross-compiled with CGO_ENABLED=0. Keeping them apart means `go vet ./...` in
// the kernel never depends on a GUI toolchain, and a broken Xcode install
// cannot stop the binary from building.
//
// The module path stays under pi-go/ so the shell may import pi-go/internal:
// Go's internal rule is about the import path prefix, not the directory tree.
module pi-go/desktop

go 1.26.0

require (
	github.com/wailsapp/wails/v3 v3.0.0
	golang.org/x/sys v0.48.0
	pi-go v0.0.0
	pi-go/channel v0.0.0
)

require (
	github.com/adrg/xdg v0.5.3 // indirect
	github.com/coder/websocket v1.8.14 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/larksuite/oapi-sdk-go/v3 v3.13.0 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
)

replace pi-go => ..

replace pi-go/channel => ../channel
