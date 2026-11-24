// Module pi-go/channel is the IM gateway: Feishu / QQ / WeCom adapters drive
// the same kernel the CLI and the window run, from chat platforms.
//
// It is a separate module for the same reason desktop/ is: it needs a
// third-party SDK (the Feishu long connection), while the kernel module's
// dependency gate keeps internal/ stdlib-only so every offline test means
// what it says. Keeping the gateway out of pi-go means `go vet ./...` and
// tests/deps/run.sh in the kernel never see this module, and a broken
// upstream SDK cannot stop the CLI from building.
//
// The module path stays under pi-go/ so the gateway may import pi-go/internal
// (assembly, wire): Go's internal rule is about the import path prefix, not
// the directory tree.
module pi-go/channel

go 1.26.0

replace pi-go => ..

require (
	github.com/larksuite/oapi-sdk-go/v3 v3.13.0
	pi-go v0.0.0-00010101000000-000000000000
)

require (
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/gorilla/websocket v1.5.0 // indirect
)
