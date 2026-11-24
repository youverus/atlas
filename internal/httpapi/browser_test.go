package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

type stubBrowserHost struct {
	Host
	state BrowserState
}

func (s *stubBrowserHost) BrowserState() (BrowserState, error) { return s.state, nil }
func (s *stubBrowserHost) BrowserOpen(url string) (BrowserTabInfo, error) {
	tab := BrowserTabInfo{ID: "tab-1", URL: url, Title: "Opened", Control: "user", Active: true}
	s.state.Running = true
	s.state.ActiveTab = tab.ID
	s.state.Tabs = append(s.state.Tabs, tab)
	return tab, nil
}
func (s *stubBrowserHost) BrowserNavigate(tabID, url string) (BrowserTabInfo, error) {
	for i := range s.state.Tabs {
		if s.state.Tabs[i].ID == tabID {
			s.state.Tabs[i].URL = url
			return s.state.Tabs[i], nil
		}
	}
	return BrowserTabInfo{}, nil
}
func (s *stubBrowserHost) BrowserAction(tabID, action string) error {
	for i := range s.state.Tabs {
		if s.state.Tabs[i].ID == tabID && action == "takeover" {
			s.state.Tabs[i].Control = "user"
			s.state.Tabs[i].Paused = true
		}
	}
	return nil
}
func (s *stubBrowserHost) BrowserScreenshot(tabID string) (BrowserCapture, error) {
	return BrowserCapture{TabID: tabID, MIME: "image/jpeg", Base64: "AQID", Width: 800, Height: 600}, nil
}

func TestBrowserRoutesStateOpenActionAndScreenshot(t *testing.T) {
	plain := projectServer(t, &projectHost{}, nil)
	resp, err := http.Get(plain.URL + "/api/browser")
	if err != nil {
		t.Fatalf("get without BrowserStatus: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", resp.StatusCode)
	}

	host := &stubBrowserHost{
		Host:  &projectHost{},
		state: BrowserState{Available: true, Executable: "/usr/bin/chrome"},
	}
	srv := projectServer(t, host, nil)

	post := func(path string, body any) *http.Response {
		raw, _ := json.Marshal(body)
		r, err := http.Post(srv.URL+path, "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("post %s: %v", path, err)
		}
		return r
	}

	rOpen := post("/api/browser/open", map[string]string{"url": "https://example.com"})
	defer rOpen.Body.Close()
	if rOpen.StatusCode != http.StatusOK {
		t.Fatalf("open status %d", rOpen.StatusCode)
	}
	var opened BrowserTabInfo
	if err := json.NewDecoder(rOpen.Body).Decode(&opened); err != nil {
		t.Fatalf("decode opened: %v", err)
	}
	if opened.ID != "tab-1" || opened.URL != "https://example.com" {
		t.Fatalf("unexpected opened tab: %+v", opened)
	}

	rAct := post("/api/browser/action", map[string]string{"tabId": "tab-1", "action": "takeover"})
	defer rAct.Body.Close()
	if rAct.StatusCode != http.StatusOK {
		t.Fatalf("action status %d", rAct.StatusCode)
	}
	var state BrowserState
	if err := json.NewDecoder(rAct.Body).Decode(&state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if len(state.Tabs) != 1 || !state.Tabs[0].Paused {
		t.Fatalf("expected paused tab after takeover, got %+v", state)
	}

	rShot, err := http.Get(srv.URL + "/api/browser/screenshot?tabId=tab-1")
	if err != nil {
		t.Fatalf("get screenshot: %v", err)
	}
	defer rShot.Body.Close()
	var cap BrowserCapture
	if err := json.NewDecoder(rShot.Body).Decode(&cap); err != nil {
		t.Fatalf("decode capture: %v", err)
	}
	if cap.Base64 != "AQID" || cap.Width != 800 {
		t.Fatalf("unexpected capture: %+v", cap)
	}
}
