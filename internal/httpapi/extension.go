package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
)

// ConnectionInfo is what GET /api/connection reports: how a browser extension
// should reach this server. Token is empty when none was issued, so an options
// page can tell "no credential yet" from "wrong credential".
type ConnectionInfo struct {
	URL   string `json:"url"`
	Token string `json:"token,omitempty"`
}

// extensionPairPath is the one route an unapproved extension may reach. It is
// the knock on the door: the request rings the ordinary approval card on the
// served page, and an approval records the extension origin for later
// requests, token-free.
const extensionPairPath = "/api/extension/pair"

// ExtensionApprovals admits browser extensions a human approved and records
// new approvals. The composition root owns the durable copy (an allowlist
// file); httpapi only asks and reports.
type ExtensionApprovals interface {
	// Approved reports whether an extension Origin was approved before. It
	// runs on every guarded request, so implementations answer from memory.
	Approved(origin string) bool
	// Approve records one origin durably, after the reader allowed the
	// pairing question.
	Approve(origin string) error
}

// serveConnection hands the served page the extension's connection material.
// Guarded as any other route, the answer is only readable by a caller that was
// already inside — the served page itself, or an extension that already holds
// the token — so echoing the token back is not a way to mint credentials.
func (s *Server) serveConnection(w http.ResponseWriter, r *http.Request) {
	info := ConnectionInfo{URL: s.URL(), Token: s.extensionToken}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(info)
}

// serveExtensionPair runs the pairing handshake. The guard already confirmed
// the caller is an extension context; this handler turns the knock into an
// ordinary approval card — Subject names the ask, Tool carries the origin so
// the reader can see which extension is knocking — and, on an approval,
// persists the origin through ExtensionApprovals.
//
// The POST blocks until the card is answered (or the approval timeout denies
// it), because the extension's options page is waiting on this very answer to
// report success. A second knock from an already-approved origin returns at
// once, so a lost reply only costs one retry.
func (s *Server) serveExtensionPair(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if !extensionOrigin(origin) {
		http.Error(w, "httpapi: unexpected origin", http.StatusForbidden)
		return
	}
	if s.extensionApprovals == nil {
		http.Error(w, "httpapi: extension pairing is not configured", http.StatusNotImplemented)
		return
	}
	if s.extensionApprovals.Approved(origin) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	decision := s.Ask(Question{Subject: "浏览器扩展请求连接", Tool: origin})
	if !decision.Allowed {
		http.Error(w, "httpapi: extension pairing denied", http.StatusForbidden)
		return
	}
	if err := s.extensionApprovals.Approve(origin); err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// extensionAdmitted decides the one cross-origin caller the guard lets past:
// a request from a browser extension context carrying a token this server
// issued. Both halves matter — the origin proves the request was made by an
// installed extension (a web page cannot present an extension scheme), and the
// token proves it is one this shell wants. The global AuthToken also admits
// such a request, so a headless `atlas -serve` needs no second credential.
func (s *Server) extensionAdmitted(origin string, r *http.Request) bool {
	if s.extensionToken == "" && s.authToken == "" {
		return false
	}
	if !extensionOrigin(origin) {
		return false
	}
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, prefix) {
		return false
	}
	presented := strings.TrimPrefix(auth, prefix)
	if s.extensionToken != "" &&
		subtle.ConstantTimeCompare([]byte(presented), []byte(s.extensionToken)) == 1 {
		return true
	}
	return s.authToken != "" &&
		subtle.ConstantTimeCompare([]byte(presented), []byte(s.authToken)) == 1
}

// extensionOrigin reports whether an Origin header names a browser extension
// context. Browsers derive the scheme from the extension platform, so a page
// on any other origin cannot present one.
func extensionOrigin(origin string) bool {
	for _, scheme := range []string{"chrome-extension://", "moz-extension://", "safari-web-extension://"} {
		if strings.HasPrefix(origin, scheme) {
			return true
		}
	}
	return false
}
