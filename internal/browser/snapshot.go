package browser

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sync"
)

const (
	defaultInspectMaxNodes     = 100
	defaultInspectMaxTextChars = 8000
	defaultSnapshotMaxNodes    = 400
	defaultSnapshotMaxText     = 40000
)

// Rect describes an element's viewport bounding box in CSS pixels.
type Rect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// SnapshotNode describes one visible interactive element assigned an eN ref.
type SnapshotNode struct {
	Ref         string `json:"ref"`
	Role        string `json:"role"`
	Name        string `json:"name"`
	Value       string `json:"value,omitempty"`
	Description string `json:"description,omitempty"`
	Disabled    bool   `json:"disabled,omitempty"`
	Focused     bool   `json:"focused,omitempty"`
	Checked     *bool  `json:"checked,omitempty"`
	Level       int    `json:"level,omitempty"`
	FrameID     string `json:"frameId,omitempty"`
	FrameURL    string `json:"frameUrl,omitempty"`
	Bounds      *Rect  `json:"bounds,omitempty"`
}

// Snapshot is one point-in-time capture of visible text and interactive refs.
type Snapshot struct {
	TabID               string         `json:"tabId"`
	SnapshotID          string         `json:"snapshotId"`
	Generation          int            `json:"generation"`
	URL                 string         `json:"url"`
	Title               string         `json:"title"`
	Text                string         `json:"text"`
	Nodes               []SnapshotNode `json:"nodes"`
	Truncated           bool           `json:"truncated"`
	UntrustedWebContent bool           `json:"untrustedWebContent"`
}

// ScreenshotResult carries a base64 PNG/JPEG image captured from a browser tab.
type ScreenshotResult struct {
	TabID               string `json:"tabId"`
	MIME                string `json:"mime"`
	Base64              string `json:"base64"`
	Width               int    `json:"width"`
	Height              int    `json:"height"`
	Mode                string `json:"mode"`
	Generation          int    `json:"generation"`
	UntrustedWebContent bool   `json:"untrustedWebContent"`
}

// InspectTruncated reports whether any section of an inspect payload was capped.
type InspectTruncated struct {
	Text       bool `json:"text"`
	Nodes      bool `json:"nodes"`
	Screenshot bool `json:"screenshot"`
	Tabs       bool `json:"tabs"`
}

// InspectResult is the compact page-state payload returned by browser_inspect.
type InspectResult struct {
	InspectionID        string           `json:"inspectionId"`
	TabID               string           `json:"tabId"`
	Generation          int              `json:"generation"`
	Tabs                []TabSummary     `json:"tabs"`
	URL                 string           `json:"url"`
	Title               string           `json:"title"`
	Loading             bool             `json:"loading"`
	Changed             bool             `json:"changed"`
	Snapshot            *Snapshot        `json:"snapshot,omitempty"`
	Truncated           InspectTruncated `json:"truncated"`
	UntrustedWebContent bool             `json:"untrustedWebContent"`
}

type storedSnapshot struct {
	id         string
	generation int
	nodes      map[string]SnapshotNode
	frameIDs   map[string]string
	offsets    map[string]Rect
}

type inspectionRecord struct {
	tabID       string
	generation  int
	contentHash string
}

type inspectionStore struct {
	mu      sync.Mutex
	records map[string]inspectionRecord
}

func newInspectionStore() *inspectionStore {
	return &inspectionStore{records: make(map[string]inspectionRecord)}
}

func (s *inspectionStore) Record(tabID string, generation int, contentHash, sinceID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := true
	if sinceID != "" {
		if prev, ok := s.records[sinceID]; ok && prev.tabID == tabID && prev.generation == generation && prev.contentHash == contentHash {
			changed = false
		}
	}
	id := newUUID()
	if len(s.records) > 256 {
		s.records = make(map[string]inspectionRecord)
	}
	s.records[id] = inspectionRecord{tabID: tabID, generation: generation, contentHash: contentHash}
	return id, changed
}

func (s *inspectionStore) ClearTab(tabID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, rec := range s.records {
		if rec.tabID == tabID {
			delete(s.records, id)
		}
	}
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func hashInspectState(generation int, url, title string, loading bool, snap *Snapshot) string {
	payload, _ := json.Marshal(struct {
		Generation int            `json:"g"`
		URL        string         `json:"u"`
		Title      string         `json:"t"`
		Loading    bool           `json:"l"`
		Text       string         `json:"x"`
		Nodes      []SnapshotNode `json:"n"`
	}{
		Generation: generation,
		URL:        url,
		Title:      title,
		Loading:    loading,
		Text:       snap.Text,
		Nodes:      snap.Nodes,
	})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

var sensitiveControlRe = regexp.MustCompile(`(?i)\b(?:buy|purchase|pay|checkout|delete|remove|send|submit|authorize|approve|confirm|download|upload|sign[ -]?in|log[ -]?in)\b`)

func isSensitiveNode(node SnapshotNode) bool {
	return sensitiveControlRe.MatchString(node.Name)
}

type rawSnapshotPayload struct {
	Text          string         `json:"text"`
	Nodes         []SnapshotNode `json:"nodes"`
	TextTruncated bool           `json:"textTruncated"`
	NodesTrunc    bool           `json:"nodesTruncated"`
}

func createSnapshotScript(snapshotID string, maxNodes, maxTextChars, startIndex int) string {
	tokenJSON, _ := json.Marshal(snapshotID)
	return fmt.Sprintf(`(() => {
  const token = %s;
  for (const old of document.querySelectorAll('[data-atlas-browser-ref]')) old.removeAttribute('data-atlas-browser-ref');
  const selector = 'a,button,input,textarea,select,summary,[role],[tabindex],[contenteditable="true"]';
  const all = Array.from(document.querySelectorAll(selector));
  const nodes = [];
  let nodesTruncated = false;
  let index = %d;
  for (const element of all) {
    const rect = element.getBoundingClientRect();
    const style = getComputedStyle(element);
    if (rect.width <= 0 || rect.height <= 0 || style.visibility === 'hidden' || style.display === 'none') continue;
    if (nodes.length >= %d) {
      nodesTruncated = true;
      break;
    }
    const ref = 'e' + (++index);
    element.setAttribute('data-atlas-browser-ref', token + ':' + ref);
    const tag = element.tagName.toLowerCase();
    const autocomplete = (element.getAttribute('autocomplete') || '').toLowerCase();
    const secretInput = tag === 'input' && (element.type === 'password' || /(?:password|one-time-code|cc-number|cc-csc)/.test(autocomplete));
    const safeValue = secretInput || (tag === 'input' && element.type === 'file') ? '' : (typeof element.value === 'string' ? element.value : '');
    const role = element.getAttribute('role') || ({a:'link',button:'button',input:(element.type === 'checkbox' ? 'checkbox' : element.type === 'radio' ? 'radio' : element.type === 'file' ? 'file-upload' : element.type === 'password' ? 'password' : 'textbox'),textarea:'textbox',select:'combobox',summary:'button'}[tag] || 'generic');
    const name = (element.getAttribute('aria-label') || element.getAttribute('alt') || element.getAttribute('placeholder') || element.innerText || safeValue || element.getAttribute('title') || '').trim().replace(/\s+/g, ' ').slice(0, 300);
    nodes.push({
      ref,
      role,
      name,
      value: secretInput ? undefined : safeValue.slice(0, 500),
      description: secretInput ? 'Sensitive value redacted' : undefined,
      disabled: Boolean(element.disabled),
      focused: document.activeElement === element,
      checked: typeof element.checked === 'boolean' ? element.checked : undefined,
      level: Number(element.getAttribute('aria-level')) || undefined,
      bounds: { x: Math.round(rect.x), y: Math.round(rect.y), width: Math.round(rect.width), height: Math.round(rect.height) }
    });
  }
  const rawText = (document.body?.innerText || '').replace(/\r/g, '');
  const textTruncated = rawText.length > %d;
  return { text: rawText.slice(0, %d), nodes, textTruncated, nodesTruncated };
})()`, string(tokenJSON), startIndex, maxNodes, maxTextChars, maxTextChars)
}

type elementPointResult struct {
	X           float64 `json:"x"`
	Y           float64 `json:"y"`
	Width       float64 `json:"width"`
	Height      float64 `json:"height"`
	ExternalURL string  `json:"externalUrl,omitempty"`
}

func elementPointScript(snapshotID, ref string, focus bool) string {
	targetJSON, _ := json.Marshal(snapshotID + ":" + ref)
	focusLiteral := "false"
	if focus {
		focusLiteral = "true"
	}
	return fmt.Sprintf(`(() => {
  const element = document.querySelector('[data-atlas-browser-ref=' + CSS.escape(%s) + ']');
  if (!element || !element.isConnected) return null;
  if (element.disabled) return null;
  let rect = element.getBoundingClientRect();
  if (rect.width <= 0 || rect.height <= 0) return null;
  if (rect.bottom <= 0 || rect.right <= 0 || rect.top >= innerHeight || rect.left >= innerWidth) {
    element.scrollIntoView({ block: 'center', inline: 'center', behavior: 'instant' });
    rect = element.getBoundingClientRect();
  }
  if (%s && typeof element.focus === 'function') element.focus();
  let externalUrl = '';
  const anchor = element.closest ? element.closest('a[href]') : null;
  if (anchor && anchor.href && !/^(?:https?:|about:|javascript:|#)/i.test(anchor.getAttribute('href') || '')) {
    externalUrl = anchor.href;
  }
  return { x: rect.left + rect.width / 2, y: rect.top + rect.height / 2, width: rect.width, height: rect.height, externalUrl };
})()`, string(targetJSON), focusLiteral)
}
