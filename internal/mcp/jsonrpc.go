// Package mcp is the client side of the Model Context Protocol: it connects to
// the servers named in the config, lists their tools, and calls them.
//
// It is a host, not a server: atlas consumes other people's MCP servers and
// never offers its own. The protocol is fixed, so this package owns no API
// design — what it does own is everything the spec leaves to the implementation:
// process lifecycle for stdio servers, one tool name per remote tool, and the
// conversion of a result into something a model can read.
//
// Only the standard library, internal/tool, internal/wire and internal/sse are
// imported. The official Go SDK is third party, and the kernel does not take
// third-party dependencies; the surface needed here is small enough that
// writing it is cheaper than an exception to that rule.
package mcp

import (
	"encoding/json"
	"fmt"
)

// jsonrpcVersion is the only value JSON-RPC 2.0 has.
const jsonrpcVersion = "2.0"

// JSON-RPC and MCP error codes this client distinguishes. The three reserved
// MCP codes (-32020..-32022) are about the version and header negotiation of a
// stateless revision this client does not speak, so they are not here.
const (
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInternal       = -32603
)

// rpcRequest is one call this client makes. ID is omitted for notifications:
// the spec's only difference between a request and a notification is whether
// the id is there.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcResponse is one answer. Result and Error are mutually exclusive by the
// spec, and both stay raw: the caller decodes the shape it asked for.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is the error object. Code is the part a caller can act on; Message
// and Data belong to the server.
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	if len(e.Data) > 0 {
		return fmt.Sprintf("json-rpc error %d: %s (%s)", e.Code, e.Message, e.Data)
	}
	return fmt.Sprintf("json-rpc error %d: %s", e.Code, e.Message)
}

// incoming is any message that arrives from a server. One decode covers all
// three shapes, because the discriminator is which fields are present rather
// than which type the JSON was written for.
type incoming struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	ID      *int64          `json:"id"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

// isRequest reports a message that expects an answer. Servers in this version
// have nothing to ask: the client declares no capabilities.
func (m incoming) isRequest() bool { return m.Method != "" && m.ID != nil }

// isNotification reports a message that expects no answer.
func (m incoming) isNotification() bool { return m.Method != "" && m.ID == nil }

// isResponse reports the answer to one of this client's calls.
func (m incoming) isResponse() bool { return m.Method == "" && m.ID != nil }
