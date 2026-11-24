package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubApprovals 是 ExtensionApprovals 的最小实现：内存集合代替 allowlist 文件，
// 让测试能在不落盘的情况下断言准入与记录行为。
type stubApprovals struct {
	mu       sync.Mutex
	approved map[string]bool
}

func (s *stubApprovals) Approved(origin string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.approved[origin]
}

func (s *stubApprovals) Approve(origin string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.approved[origin] = true
	return nil
}

// do sends a request with an Origin and an optional Authorization header and
// returns the status, so the guard tests can speak in the extension's terms.
// The body is a placeholder: the guard runs before any handler parses it.
func do(t *testing.T, server *Server, method, path, origin, auth string) int {
	t.Helper()
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	request, err := http.NewRequest(method, httpServer.URL+path, strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	if auth != "" {
		request.Header.Set("Authorization", auth)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer response.Body.Close()
	return response.StatusCode
}

// admitted reports whether the guard let the request through: without a Turn
// the routes answer 501 or 400 of their own, and 401/403 mean the guard said no.
func admitted(status int) bool {
	return status != http.StatusForbidden && status != http.StatusUnauthorized
}

func TestAnExtensionWithTheTokenIsAdmitted(t *testing.T) {
	server := New(Options{ExtensionToken: "ext-token"})
	if status := do(t, server, http.MethodPost, "/api/ask", "chrome-extension://abcdef", "Bearer ext-token"); !admitted(status) {
		t.Fatalf("status = %d, want past the guard", status)
	}
	if status := do(t, server, http.MethodGet, "/api/state", "chrome-extension://abcdef", "Bearer ext-token"); status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
}

func TestAnExtensionWithTheWrongTokenIsRefused(t *testing.T) {
	server := New(Options{ExtensionToken: "ext-token"})
	if status := do(t, server, http.MethodPost, "/api/ask", "chrome-extension://abcdef", "Bearer other"); status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", status)
	}
	if status := do(t, server, http.MethodPost, "/api/ask", "chrome-extension://abcdef", ""); status != http.StatusForbidden {
		t.Fatalf("status without token = %d, want 403", status)
	}
}

func TestAWebPageCannotUseTheExtensionToken(t *testing.T) {
	// The origin half is what keeps a web page out even when it somehow learned
	// the token: the browser stamps a page's requests with the page's origin.
	server := New(Options{ExtensionToken: "ext-token"})
	for _, origin := range []string{"https://attacker.example", "http://127.0.0.1:1", "chrome-extension:spoof"} {
		if status := do(t, server, http.MethodPost, "/api/ask", origin, "Bearer ext-token"); status != http.StatusForbidden {
			t.Fatalf("origin %q: status = %d, want 403", origin, status)
		}
	}
}

func TestAnExtensionNeedsATokenWhenOneIsIssued(t *testing.T) {
	cases := []struct {
		name    string
		options Options
		auth    string
		wantIn  bool
	}{
		{"no tokens configured", Options{}, "Bearer anything", false},
		{"extension token, wrong presented", Options{ExtensionToken: "ext-token"}, "Bearer other", false},
		{"global token, wrong presented", Options{AuthToken: "serve-token"}, "Bearer ext-token", false},
		// A headless `atlas -serve` has no separate extension token; the global
		// one admits the extension so it needs no second credential.
		{"global token, right presented", Options{AuthToken: "serve-token"}, "Bearer serve-token", true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := New(testCase.options)
			status := do(t, server, http.MethodGet, "/api/state", "moz-extension://abc", testCase.auth)
			if admitted(status) != testCase.wantIn {
				t.Fatalf("status = %d, want in = %v", status, testCase.wantIn)
			}
		})
	}
}

func TestAReboundExtensionOriginIsStillRefused(t *testing.T) {
	// The Host check runs before the origin check, so an extension token cannot
	// launder a rebound request through.
	server := New(Options{ExtensionToken: "ext-token"})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	request, err := http.NewRequest(http.MethodPost, httpServer.URL+"/api/ask", strings.NewReader(`{"prompt":"hi"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Host = "attacker.example:80"
	request.Header.Set("Origin", "chrome-extension://abcdef")
	request.Header.Set("Authorization", "Bearer ext-token")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.StatusCode)
	}
}

func TestConnectionReportsTheExtensionMaterial(t *testing.T) {
	server := New(Options{ExtensionToken: "ext-token"})
	if _, err := server.Listen(); err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer server.Close()

	response, err := http.Get(server.URL() + "/api/connection")
	if err != nil {
		t.Fatalf("get connection: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	var info ConnectionInfo
	if err := json.Unmarshal(body, &info); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if info.URL != server.URL() {
		t.Fatalf("url = %q, want %q", info.URL, server.URL())
	}
	if info.Token != "ext-token" {
		t.Fatalf("token = %q, want the issued one", info.Token)
	}
}

func TestConnectionOmitsAnUnissuedToken(t *testing.T) {
	server := New(Options{})
	if _, err := server.Listen(); err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer server.Close()
	response, err := http.Get(server.URL() + "/api/connection")
	if err != nil {
		t.Fatalf("get connection: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(body), "token") {
		t.Fatalf("token present without an ExtensionToken option: %s", body)
	}
}

// pairAndAnswer sends the pairing POST in the background and answers the
// approval card it rings, so the test plays the reader on the desktop side.
// It returns the status the pair request finished with.
func pairAndAnswer(t *testing.T, server *Server, httpServer *httptest.Server, origin string, allowed bool) int {
	t.Helper()
	result := make(chan int, 1)
	go func() {
		request, err := http.NewRequest(http.MethodPost, httpServer.URL+extensionPairPath, nil)
		if err != nil {
			result <- -1
			return
		}
		request.Header.Set("Origin", origin)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			result <- -1
			return
		}
		defer response.Body.Close()
		result <- response.StatusCode
	}()

	var item pending
	deadline := time.Now().Add(2 * time.Second)
	for {
		if items := server.approvals.list(); len(items) > 0 {
			item = items[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pairing did not ring the approval card")
		}
		time.Sleep(5 * time.Millisecond)
	}

	request, err := http.NewRequest(http.MethodPost, httpServer.URL+"/api/approval",
		strings.NewReader(`{"id":"`+item.id+`","allowed":`+boolJSON(allowed)+`}`))
	if err != nil {
		t.Fatalf("new approval request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("answer approval: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("answer approval = %d, want 204", response.StatusCode)
	}
	return <-result
}

func boolJSON(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func TestPairingPersistsAnApproval(t *testing.T) {
	approvals := &stubApprovals{approved: map[string]bool{}}
	server := New(Options{ExtensionApprovals: approvals})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	origin := "chrome-extension://abc"
	if status := pairAndAnswer(t, server, httpServer, origin, true); status != http.StatusNoContent {
		t.Fatalf("pair status = %d, want 204", status)
	}
	if !approvals.Approved(origin) {
		t.Fatal("the approval was not recorded")
	}
	// 批准之后免 token 直连。
	if status := do(t, server, http.MethodGet, "/api/state", origin, ""); status != http.StatusOK {
		t.Fatalf("status after approval = %d, want 200", status)
	}
	// 再敲一次门立即返回，不重复弹卡。
	if status := do(t, server, http.MethodPost, extensionPairPath, origin, ""); status != http.StatusNoContent {
		t.Fatalf("second pair status = %d, want 204", status)
	}
}

func TestPairingDeniedRecordsNothing(t *testing.T) {
	approvals := &stubApprovals{approved: map[string]bool{}}
	server := New(Options{ExtensionApprovals: approvals})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	origin := "chrome-extension://abc"
	if status := pairAndAnswer(t, server, httpServer, origin, false); status != http.StatusForbidden {
		t.Fatalf("pair status = %d, want 403", status)
	}
	if approvals.Approved(origin) {
		t.Fatal("a denied pairing was recorded")
	}
	if status := do(t, server, http.MethodGet, "/api/state", origin, ""); status != http.StatusForbidden {
		t.Fatalf("status after denial = %d, want 403", status)
	}
}

func TestPairingOnlyAnswersExtensionOrigins(t *testing.T) {
	server := New(Options{ExtensionApprovals: &stubApprovals{approved: map[string]bool{}}})
	if status := do(t, server, http.MethodPost, extensionPairPath, "", ""); status != http.StatusForbidden {
		t.Fatalf("no origin status = %d, want 403", status)
	}
	if status := do(t, server, http.MethodPost, extensionPairPath, "https://attacker.example", ""); status != http.StatusForbidden {
		t.Fatalf("web page status = %d, want 403", status)
	}
}

func TestPairingWithoutApprovalsIsNotImplemented(t *testing.T) {
	server := New(Options{})
	if status := do(t, server, http.MethodPost, extensionPairPath, "chrome-extension://abc", ""); status != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", status)
	}
}
