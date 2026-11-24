package device

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"pi-go/internal/httpapi"
)

// WrapRemoteKey encodes a device ID and remote file/directory path into a
// globally unique session or directory identifier that never collides with
// local paths.
func WrapRemoteKey(deviceID, remotePath string) string {
	if deviceID == "" {
		return remotePath
	}
	if strings.HasPrefix(remotePath, "remote://") {
		return remotePath
	}
	p := strings.TrimSpace(remotePath)
	if p == "" || p == "/" {
		return "remote://" + deviceID + "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return "remote://" + deviceID + p
}

// ParseRemoteKey splits a wrapped identifier (`remote://<deviceID>/...`) back
// into device ID and POSIX path on the remote host.
func ParseRemoteKey(key string) (deviceID, remotePath string, ok bool) {
	if !strings.HasPrefix(key, "remote://") {
		return "", "", false
	}
	rest := strings.TrimPrefix(key, "remote://")
	devID, tail, hasSlash := strings.Cut(rest, "/")
	if devID == "" {
		return "", "", false
	}
	if !hasSlash || tail == "" {
		return devID, "/", true
	}
	return devID, "/" + tail, true
}

type approvalRecord struct {
	RawID string
	Item  httpapi.PendingQuestionItem
}

type remoteClient struct {
	deviceID   string
	deviceName string
	baseURL    string
	token      string
	http       *http.Client

	mu        sync.RWMutex
	running   map[string]string
	sideRun   map[string]bool
	approvals map[string]approvalRecord
	logs      []string
	lastState struct {
		session   string
		directory string
		archived  bool
		model     string
	}
}

func newRemoteClient(deviceID, deviceName, baseURL, token string) *remoteClient {
	return &remoteClient{
		deviceID:   deviceID,
		deviceName: deviceName,
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		http:       &http.Client{Timeout: 30 * time.Second},
		running:    map[string]string{},
		sideRun:    map[string]bool{},
		approvals:  map[string]approvalRecord{},
	}
}

func (c *remoteClient) doJSON(ctx context.Context, method, endpoint string, reqBody any, respBody any) (int, error) {
	var bodyReader io.Reader
	if reqBody != nil {
		raw, err := json.Marshal(reqBody)
		if err != nil {
			return 0, err
		}
		bodyReader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+endpoint, bodyReader)
	if err != nil {
		return 0, err
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	rawResp, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(rawResp))
		if msg == "" {
			msg = resp.Status
		}
		return resp.StatusCode, fmt.Errorf("%s", msg)
	}
	if respBody != nil && len(rawResp) > 0 {
		if err := json.Unmarshal(rawResp, respBody); err != nil {
			return resp.StatusCode, fmt.Errorf("device %s decode %s: %w", c.deviceID, endpoint, err)
		}
	}
	return resp.StatusCode, nil
}

type remoteStatePayload struct {
	httpapi.Facts
	Session         string                        `json:"session"`
	Running         bool                          `json:"running"`
	TurnID          string                        `json:"turnId"`
	RunningSessions []string                      `json:"runningSessions"`
	Archived        bool                          `json:"archived"`
	Pending         []httpapi.PendingQuestionItem `json:"pending"`
}

func (c *remoteClient) fetchState(ctx context.Context) (remoteStatePayload, int, error) {
	var state remoteStatePayload
	status, err := c.doJSON(ctx, http.MethodGet, "/api/state", nil, &state)
	if err != nil {
		return state, status, err
	}
	c.syncStateCache(state)
	return state, status, nil
}

func (c *remoteClient) syncStateCache(state remoteStatePayload) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if state.Session != "" {
		c.lastState.session = WrapRemoteKey(c.deviceID, state.Session)
	}
	c.lastState.directory = state.Directory
	c.lastState.archived = state.Archived
	c.lastState.model = state.Model

	nextRunning := map[string]string{}
	for _, session := range state.RunningSessions {
		if session != "" {
			key := WrapRemoteKey(c.deviceID, session)
			nextRunning[key] = c.running[key]
		}
	}
	if state.Running && state.Session != "" {
		nextRunning[WrapRemoteKey(c.deviceID, state.Session)] = state.TurnID
	}
	c.running = nextRunning

	nextApprovals := make(map[string]approvalRecord, len(state.Pending))
	for _, item := range state.Pending {
		rawID := item.ID
		item.ID = c.wrapApprovalID(rawID)
		if item.Session != "" {
			item.Session = WrapRemoteKey(c.deviceID, item.Session)
		}
		nextApprovals[item.ID] = approvalRecord{RawID: rawID, Item: item}
	}
	c.approvals = nextApprovals
}

func (c *remoteClient) wrapApprovalID(rawID string) string {
	return "remote://" + c.deviceID + "/" + strings.TrimPrefix(rawID, "/")
}

func (c *remoteClient) runningSessionKeys() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.running))
	for key := range c.running {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func (c *remoteClient) pendingApprovals() []httpapi.PendingQuestionItem {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]httpapi.PendingQuestionItem, 0, len(c.approvals))
	for _, rec := range c.approvals {
		out = append(out, rec.Item)
	}
	return out
}

func (c *remoteClient) isSideRunning(sessionKey string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sideRun[sessionKey]
}

func (c *remoteClient) forwardAsk(ctx context.Context, rawSession, prompt string, attachments []httpapi.Attachment) error {
	payload := map[string]any{
		"session":     rawSession,
		"prompt":      prompt,
		"attachments": attachments,
	}
	_, err := c.doJSON(ctx, http.MethodPost, "/api/ask", payload, nil)
	return err
}

func (c *remoteClient) forwardApproval(ctx context.Context, wrappedID string, allowed, always bool) error {
	c.mu.Lock()
	rec, ok := c.approvals[wrappedID]
	if ok {
		delete(c.approvals, wrappedID)
	}
	c.mu.Unlock()

	rawID := rec.RawID
	if !ok {
		_, parsed, parsedOK := ParseRemoteKey(wrappedID)
		if !parsedOK {
			return fmt.Errorf("device %s: invalid remote approval id %q", c.deviceID, wrappedID)
		}
		rawID = strings.TrimPrefix(parsed, "/")
	}
	_, err := c.doJSON(ctx, http.MethodPost, "/api/approval", map[string]any{
		"id":      rawID,
		"allowed": allowed,
		"always":  always,
	}, nil)
	return err
}

func (c *remoteClient) forwardCompact(ctx context.Context, rawSession, note string) (bool, error) {
	var resp struct {
		OK bool `json:"ok"`
	}
	_, err := c.doJSON(ctx, http.MethodPost, "/api/compact", map[string]string{
		"session": rawSession,
		"note":    note,
	}, &resp)
	return resp.OK, err
}

func (c *remoteClient) forwardSideAsk(ctx context.Context, rawSession, prompt string) error {
	_, err := c.doJSON(ctx, http.MethodPost, "/api/side/ask", map[string]string{
		"session": rawSession,
		"prompt":  prompt,
	}, nil)
	return err
}

// runSSE connects to the remote `/api/events` stream and bridges `agent`,
// `status`, `approval`, `sideagent`, and `sidestatus` frames into `publish`
// until ctx cancels or the stream closes.
func (c *remoteClient) runSSE(ctx context.Context, publish func(name string, payload []byte)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/events", nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	streamClient := &http.Client{}
	resp, err := streamClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("device %s events stream: HTTP %d", c.deviceID, resp.StatusCode)
	}

	reader := bufio.NewReaderSize(resp.Body, 256*1024)
	var eventName string
	var dataLines []string
	flush := func() {
		if eventName == "" && len(dataLines) == 0 {
			return
		}
		rawData := strings.Join(dataLines, "\n")
		name := eventName
		eventName = ""
		dataLines = nil
		if name == "" || rawData == "" {
			return
		}
		if rewritten, forward := c.rewriteEvent(name, []byte(rawData)); forward {
			publish(name, rewritten)
		}
	}

	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			trimmed := strings.TrimRight(line, "\r\n")
			if trimmed == "" {
				flush()
			} else if strings.HasPrefix(trimmed, ":") {
				// SSE comment / heartbeat
			} else if after, ok := strings.CutPrefix(trimmed, "event:"); ok {
				eventName = strings.TrimSpace(after)
			} else if after, ok := strings.CutPrefix(trimmed, "data:"); ok {
				dataLines = append(dataLines, strings.TrimPrefix(after, " "))
			}
		}
		if err != nil {
			flush()
			return err
		}
	}
}

func (c *remoteClient) rewriteEvent(name string, raw []byte) ([]byte, bool) {
	switch name {
	case "agent", "status", "sideagent", "sidestatus":
		var frame map[string]any
		if err := json.Unmarshal(raw, &frame); err != nil {
			return nil, false
		}
		wrappedSess := ""
		if s, ok := frame["session"].(string); ok && s != "" {
			wrappedSess = WrapRemoteKey(c.deviceID, s)
			frame["session"] = wrappedSess
		} else {
			c.mu.RLock()
			wrappedSess = c.lastState.session
			c.mu.RUnlock()
			if wrappedSess != "" {
				frame["session"] = wrappedSess
			}
		}
		if name == "status" && wrappedSess != "" {
			running, _ := frame["running"].(bool)
			turnID, _ := frame["turnId"].(string)
			c.mu.Lock()
			if running {
				c.running[wrappedSess] = turnID
			} else {
				delete(c.running, wrappedSess)
				if turnID == "" {
					for id, rec := range c.approvals {
						if rec.Item.Session == wrappedSess {
							delete(c.approvals, id)
						}
					}
				}
			}
			c.mu.Unlock()
		}
		if name == "sidestatus" && wrappedSess != "" {
			running, _ := frame["running"].(bool)
			c.mu.Lock()
			if running {
				c.sideRun[wrappedSess] = true
			} else {
				delete(c.sideRun, wrappedSess)
			}
			c.mu.Unlock()
		}
		encoded, err := json.Marshal(frame)
		if err != nil {
			return nil, false
		}
		return encoded, true

	case "approval":
		var rawItem httpapi.PendingQuestionItem
		if err := json.Unmarshal(raw, &rawItem); err != nil || rawItem.ID == "" {
			return nil, false
		}
		wrappedID := c.wrapApprovalID(rawItem.ID)
		wrappedSess := ""
		if rawItem.Session != "" {
			wrappedSess = WrapRemoteKey(c.deviceID, rawItem.Session)
		}
		wrappedItem := httpapi.PendingQuestionItem{
			ID: wrappedID,
			Question: httpapi.Question{
				Session: wrappedSess,
				Subject: rawItem.Subject,
				Tool:    rawItem.Tool,
			},
		}
		c.mu.Lock()
		c.approvals[wrappedID] = approvalRecord{
			RawID: rawItem.ID,
			Item:  wrappedItem,
		}
		c.mu.Unlock()
		encoded, err := json.Marshal(wrappedItem)
		if err != nil {
			return nil, false
		}
		return encoded, true

	case "approval_resolved":
		var item struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &item); err != nil || item.ID == "" {
			return nil, false
		}
		item.ID = c.wrapApprovalID(item.ID)
		c.mu.Lock()
		delete(c.approvals, item.ID)
		c.mu.Unlock()
		encoded, err := json.Marshal(item)
		return encoded, err == nil

	default:
		// Do not forward remote `hello` or `facts` onto the local page stream.
		return nil, false
	}
}

func (c *remoteClient) remoteQueryURL(base string, params map[string]string) string {
	u := url.Values{}
	for k, v := range params {
		if v != "" {
			u.Set(k, v)
		}
	}
	q := u.Encode()
	if q == "" {
		return base
	}
	return base + "?" + q
}
