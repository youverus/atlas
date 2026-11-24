package browser

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestResolveAddressAndPolicy(t *testing.T) {
	u, err := ResolveAddress("example.com/docs", true)
	if err != nil || u != "https://example.com/docs" {
		t.Fatalf("ResolveAddress domain = %q, %v", u, err)
	}
	u, err = ResolveAddress("localhost:3000", true)
	if err != nil || u != "http://localhost:3000" {
		t.Fatalf("ResolveAddress localhost = %q, %v", u, err)
	}
	u, err = ResolveAddress("golang cdp", true)
	if err != nil || !strings.HasPrefix(u, "https://www.google.com/search?q=golang") {
		t.Fatalf("ResolveAddress search = %q, %v", u, err)
	}
	if _, err := ResolveAddress("example.com", false); err == nil {
		t.Fatal("expected bare domain rejected when allowSearch=false")
	}

	ctx := context.Background()
	if _, err := CheckNavigationURL(ctx, "http://127.0.0.1:8080/", URLPolicyOptions{UserInitiated: false}); err == nil {
		t.Fatal("expected agent loopback URL blocked")
	}
	if _, err := CheckNavigationURL(ctx, "http://127.0.0.1:8080/", URLPolicyOptions{UserInitiated: true}); err != nil {
		t.Fatalf("expected user loopback URL allowed, got %v", err)
	}
	if _, err := CheckNavigationURL(ctx, "https://user:pass@example.com/", URLPolicyOptions{UserInitiated: true}); err == nil {
		t.Fatal("expected URL with credentials blocked")
	}
	if _, err := CheckNavigationURL(ctx, "file:///etc/passwd", URLPolicyOptions{UserInitiated: true}); err == nil {
		t.Fatal("expected file:// URL blocked")
	}

	redacted := RedactURL("https://user:secret@example.com/cb?code=1234&page=2&token=abc")
	if strings.Contains(redacted, "secret") || strings.Contains(redacted, "1234") || strings.Contains(redacted, "abc") {
		t.Fatalf("expected sensitive fields redacted, got %q", redacted)
	}
	if !strings.Contains(redacted, "page=2") {
		t.Fatalf("expected non-sensitive param preserved, got %q", redacted)
	}
}

func TestInspectionStoreDeltaAndSensitiveMatch(t *testing.T) {
	store := newInspectionStore()
	snap := &Snapshot{Text: "hello", Nodes: []SnapshotNode{{Ref: "e1", Role: "button", Name: "Submit"}}}
	h1 := hashInspectState(1, "https://example.com", "Example", false, snap)
	id1, changed1 := store.Record("tab-1", 1, h1, "")
	if !changed1 {
		t.Fatal("initial inspection must report changed=true")
	}
	_, changed2 := store.Record("tab-1", 1, h1, id1)
	if changed2 {
		t.Fatal("identical inspection must report changed=false")
	}
	store.ClearTab("tab-1")
	_, changed3 := store.Record("tab-1", 1, h1, id1)
	if !changed3 {
		t.Fatal("cleared tab must report changed=true")
	}
	if !isSensitiveNode(SnapshotNode{Name: "Pay now"}) {
		t.Fatal("expected Pay now marked sensitive")
	}
	if isSensitiveNode(SnapshotNode{Name: "Next page"}) {
		t.Fatal("unexpected sensitive flag on Next page")
	}
}

func TestCDPClientWebSocketRoundTrip(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	done := make(chan error, 1)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			done <- acceptErr
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		req, readErr := http.ReadRequest(br)
		if readErr != nil {
			done <- readErr
			return
		}
		key := req.Header.Get("Sec-WebSocket-Key")
		sum := sha1.Sum([]byte(key + wsGUID))
		accept := base64.StdEncoding.EncodeToString(sum[:])
		_, _ = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: "+accept+"\r\n\r\n")

		// Read one masked frame from client.
		payload, err := readServerTextFrame(br)
		if err != nil {
			done <- err
			return
		}
		var rpcReq struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal(payload, &rpcReq); err != nil {
			done <- err
			return
		}
		// Send an event first, then the RPC response.
		_ = writeServerTextFrame(conn, []byte(`{"method":"Page.loadEventFired","params":{"timestamp":1}}`))
		reply, _ := json.Marshal(map[string]any{"id": rpcReq.ID, "result": map[string]any{"echo": rpcReq.Method}})
		_ = writeServerTextFrame(conn, reply)
		done <- nil
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := dialCDP(ctx, "ws://"+ln.Addr().String()+"/devtools/browser/test")
	if err != nil {
		t.Fatalf("dialCDP: %v", err)
	}
	defer client.Close()

	events := make(chan string, 1)
	unsub := client.Subscribe(func(_ string, method string, _ json.RawMessage) {
		events <- method
	})
	defer unsub()

	var out struct {
		Echo string `json:"echo"`
	}
	if err := client.Call(ctx, "", "Target.getTargets", nil, &out); err != nil {
		t.Fatalf("cdp Call: %v", err)
	}
	if out.Echo != "Target.getTargets" {
		t.Fatalf("unexpected echo %q", out.Echo)
	}
	select {
	case ev := <-events:
		if ev != "Page.loadEventFired" {
			t.Fatalf("unexpected event %q", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for CDP event")
	}
	if srvErr := <-done; srvErr != nil {
		t.Fatalf("server error: %v", srvErr)
	}
}

func readServerTextFrame(br *bufio.Reader) ([]byte, error) {
	var head [2]byte
	if _, err := io.ReadFull(br, head[:]); err != nil {
		return nil, err
	}
	length := int(head[1] & 0x7f)
	if length == 126 {
		var ext [2]byte
		if _, err := io.ReadFull(br, ext[:]); err != nil {
			return nil, err
		}
		length = int(binary.BigEndian.Uint16(ext[:]))
	}
	var mask [4]byte
	if _, err := io.ReadFull(br, mask[:]); err != nil {
		return nil, err
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(br, data); err != nil {
		return nil, err
	}
	for i := range data {
		data[i] ^= mask[i%4]
	}
	return data, nil
}

func writeServerTextFrame(w io.Writer, payload []byte) error {
	header := []byte{0x81, byte(len(payload))}
	if _, err := w.Write(header); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func TestResolveHeadlessAndLaunchArgs(t *testing.T) {
	noEnv := func(string) string { return "" }
	x11Env := func(key string) string {
		if key == "DISPLAY" {
			return ":0"
		}
		return ""
	}
	waylandEnv := func(key string) string {
		if key == "WAYLAND_DISPLAY" {
			return "wayland-0"
		}
		return ""
	}
	envOverrideTrue := func(key string) string {
		if key == "ATLAS_BROWSER_HEADLESS" {
			return "true"
		}
		return ""
	}

	if !resolveHeadless(Options{}, "linux", noEnv) {
		t.Fatal("expected headless auto-enabled on linux without DISPLAY or WAYLAND_DISPLAY")
	}
	if resolveHeadless(Options{}, "linux", x11Env) {
		t.Fatal("expected headed on linux when DISPLAY is set")
	}
	if resolveHeadless(Options{}, "linux", waylandEnv) {
		t.Fatal("expected headed on linux when WAYLAND_DISPLAY is set")
	}
	if resolveHeadless(Options{}, "darwin", noEnv) {
		t.Fatal("expected headed by default on darwin")
	}
	if !resolveHeadless(Options{}, "darwin", envOverrideTrue) {
		t.Fatal("expected ATLAS_BROWSER_HEADLESS=true to enable headless on darwin")
	}

	explicitFalse := false
	if resolveHeadless(Options{HeadlessOverride: &explicitFalse}, "linux", noEnv) {
		t.Fatal("expected explicit HeadlessOverride=false to win over linux auto-detection")
	}
	explicitTrue := true
	if !resolveHeadless(Options{HeadlessOverride: &explicitTrue}, "darwin", noEnv) {
		t.Fatal("expected explicit HeadlessOverride=true to enable headless on darwin")
	}

	headlessArgs := buildLaunchArgs("/tmp/prof", LaunchOptions{Headless: true})
	if !slicesContains(headlessArgs, "--headless=new") || !slicesContains(headlessArgs, "--disable-gpu") {
		t.Fatalf("headless args missing flags: %v", headlessArgs)
	}
	headedArgs := buildLaunchArgs("/tmp/prof", LaunchOptions{Headless: false})
	if slicesContains(headedArgs, "--headless=new") {
		t.Fatalf("headed args unexpectedly contained --headless=new: %v", headedArgs)
	}

	svc := NewService(Options{Executable: "/tmp/chrome", HeadlessOverride: &explicitTrue})
	if !svc.State().Headless {
		t.Fatal("expected Service.State().Headless = true")
	}
	svc.Configure(Options{Executable: "/tmp/chrome", HeadlessOverride: &explicitFalse})
	if svc.State().Headless {
		t.Fatal("expected Service.State().Headless = false after Configure")
	}
}

func slicesContains(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}
