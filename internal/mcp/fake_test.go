package mcp

// The fake server is this test binary run again with an environment variable set.
// Re-entry rather than a second program keeps the test offline and keeps the
// server's behavior beside the test that depends on it: a scripted server in
// another language would be one more thing to keep in step with the protocol.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	// envScenario names the behavior the fake server runs.
	envScenario = "MCP_FAKE_SCENARIO"
	// envLog is a file every request is appended to, which is how "the client
	// sent this exactly once" becomes observable.
	envLog = "MCP_FAKE_LOG"
	// envChildPID receives the pid of the grandchild the "group" scenario
	// starts, so the test can prove it died.
	envChildPID = "MCP_FAKE_CHILD_PID"
)

func TestMain(m *testing.M) {
	if scenario := os.Getenv(envScenario); scenario != "" {
		os.Exit(serveFake(scenario))
	}
	os.Exit(m.Run())
}

// serveFake is the server half. It answers the four methods this client sends and
// ignores everything else, which is exactly the surface docs/mcp.md promises.
func serveFake(scenario string) int {
	if scenario == "failed" {
		// The case a user actually hits: the wrapper starts and dies with the
		// reason on stderr. Nothing else in the process is worth doing.
		fmt.Fprintln(os.Stderr, "fake: the server needs a token")
		return 1
	}
	if scenario == "group" {
		// A stand-in for what npx or uvx really starts: the process that speaks
		// the protocol is not the last process in the group.
		child := exec.Command("sleep", "30")
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "fake: start child:", err)
			return 1
		}
		if path := os.Getenv(envChildPID); path != "" {
			_ = os.WriteFile(path, []byte(strconv.Itoa(child.Process.Pid)), 0o644)
		}
		defer child.Wait()
	}

	reader := bufio.NewReader(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	// grown records that the "grow" and "eager" scenarios have added their tool.
	// It is the difference between the first listing and the second, and it is
	// what makes "the tool the model could not see before" observable.
	grown := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			// The client closed stdin: a server exits, which is what makes the
			// graceful shutdown path in Close testable at all.
			return 0
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var request rpcRequest
		if err := json.Unmarshal([]byte(trimmed), &request); err != nil {
			fmt.Fprintln(os.Stderr, "fake: not json-rpc:", err)
			return 1
		}
		record(request.Method)
		if request.ID == 0 {
			continue
		}
		switch request.Method {
		case "initialize":
			version := protocolVersion
			switch scenario {
			case "old":
				version = "2025-06-18"
			case "alien":
				version = "1999-01-01"
			}
			if scenario == "incomplete" {
				// Half a message, no newline, then gone. This is the failure a
				// server that crashes mid-write produces.
				fmt.Fprint(os.Stdout, `{"jsonrpc":"2.0","id":1,"res`)
				return 0
			}
			result := map[string]any{
				"protocolVersion": version,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "fake", "version": "9.9.9"},
			}
			writeMessage(writer, response(request.ID, result))
		case "tools/list":
			if scenario == "failrefresh" && grown {
				// The second listing fails: the tools from the first one have
				// to survive it.
				writeMessage(writer, rpcResponse{JSONRPC: jsonrpcVersion, ID: &request.ID, Error: &rpcError{
					Code: codeInternal, Message: "fake: tools/list is not available right now",
				}})
				continue
			}
			writeMessage(writer, listAnswer(scenario, grown, request.ID, request.Params))
			if scenario == "eager" && !grown {
				// The tool exists before the client has anywhere to put the
				// notification: the window between the first listing and the
				// hook, which a real server hits whenever it is slow to start.
				grown = true
				writeChanged(writer)
			}
		case "tools/call":
			switch scenario {
			case "progress":
				writeProgress(writer, request)
			case "slow":
				// Not a single notification: the case the idle budget exists for,
				// and the one a test has to be able to produce deterministically.
				time.Sleep(300 * time.Millisecond)
			}
			writeMessage(writer, callAnswer(scenario, request.ID))
			if (scenario == "grow" || scenario == "failrefresh") && !grown {
				// A tool that installs tools: the server is mid-conversation, so
				// the notification lands with nothing else in flight.
				grown = true
				writeChanged(writer)
			}
		default:
			// The client is not supposed to ask anything else; answering an
			// error makes that mistake a test failure instead of a timeout.
			writeMessage(writer, rpcResponse{JSONRPC: jsonrpcVersion, ID: &request.ID, Error: &rpcError{
				Code: codeMethodNotFound, Message: "fake: no such method",
			}})
		}
	}
}

// progressTick is the gap between two notifications of the "progress" scenario.
// It is small enough that a client which does not restart its clock on them
// cannot finish the call, and large enough to survive a loaded machine.
const progressTick = 25 * time.Millisecond

// writeProgress emits what a tool that reports its work sends: five ticks before
// the answer. The token is echoed exactly as it arrived, which is what the spec
// asks for and what makes the client's string comparison a real test.
func writeProgress(writer *bufio.Writer, request rpcRequest) {
	var params struct {
		Meta struct {
			ProgressToken any `json:"progressToken"`
		} `json:"_meta"`
	}
	_ = json.Unmarshal(request.Params, &params)
	for step := 1; step <= 5; step++ {
		writeMessage(writer, rpcRequest{
			JSONRPC: jsonrpcVersion,
			Method:  methodProgress,
			Params: mustJSON(map[string]any{
				"progressToken": params.Meta.ProgressToken,
				"progress":      step,
				"total":         5,
				"message":       fmt.Sprintf("step %d of 5", step),
			}),
		})
		time.Sleep(progressTick)
	}
}

// mustJSON encodes a test payload, or fails the fake server loudly.
func mustJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

// writeChanged sends the notification that says the tool list is not what it was.
func writeChanged(writer *bufio.Writer) {
	writeMessage(writer, rpcRequest{JSONRPC: jsonrpcVersion, Method: methodToolsChanged})
}

// listAnswer describes tools. The "paged" scenario answers one tool per page to
// prove the cursor loop is followed, and "grow" and "eager" answer a longer list
// once their tool exists.
func listAnswer(scenario string, grown bool, id int64, params json.RawMessage) rpcResponse {
	tool := func(name, description string) map[string]any {
		return map[string]any{
			"name":        name,
			"description": description,
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"path": map[string]any{"type": "string"},
			}},
		}
	}
	if scenario == "grow" || scenario == "eager" {
		tools := []any{tool("read_file", "read a file"), tool("add_tool", "adds a tool")}
		if grown {
			tools = append(tools, tool("fresh_tool", "declared after the first listing"))
		}
		return response(id, map[string]any{"tools": tools})
	}
	if scenario != "paged" {
		return response(id, map[string]any{"tools": []any{
			tool("read_file", "read a file"),
			tool("list_dir", "list a directory"),
		}})
	}

	var cursor struct {
		Cursor string `json:"cursor"`
	}
	_ = json.Unmarshal(params, &cursor)
	if cursor.Cursor == "" {
		return response(id, map[string]any{
			"tools":      []any{tool("first", "the first page")},
			"nextCursor": "page-2",
		})
	}
	return response(id, map[string]any{"tools": []any{tool("second", "the second page")}})
}

// callAnswer returns a result with every block type this client maps, so one test
// covers the whole conversion.
func callAnswer(scenario string, id int64) rpcResponse {
	if scenario == "crashtool" {
		// A server-side failure: the call happened, the tool did not work.
		return rpcResponse{JSONRPC: jsonrpcVersion, ID: &id, Error: &rpcError{Code: -32000, Message: "boom"}}
	}
	return response(id, map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": "the answer"},
			map[string]any{"type": "image", "data": "aGk=", "mimeType": "image/png"},
			map[string]any{"type": "audio", "data": "aGk=", "mimeType": "audio/wav"},
			map[string]any{"type": "resource_link", "uri": "file:///notes.txt", "name": "notes"},
			map[string]any{"type": "resource", "resource": map[string]any{
				"uri": "file:///report.txt", "mimeType": "text/plain", "text": "the report",
			}},
		},
		"structuredContent": map[string]any{"rows": 3},
		"isError":           true,
	})
}

// response wraps a result for one request id.
func response(id int64, result any) rpcResponse {
	encoded, err := json.Marshal(result)
	if err != nil {
		panic(err)
	}
	return rpcResponse{JSONRPC: jsonrpcVersion, ID: &id, Result: encoded}
}

// writeMessage frames one message. The "framing" scenario writes the noise a
// sloppy server produces around a legal message: a bare carriage return, an empty
// line, and a final CRLF.
func writeMessage(writer *bufio.Writer, message any) {
	encoded, err := json.Marshal(message)
	if err != nil {
		panic(err)
	}
	if os.Getenv(envScenario) == "framing" {
		fmt.Fprint(writer, "\r\n\n")
		writer.Write(encoded)
		fmt.Fprint(writer, "\r\n")
	} else {
		writer.Write(encoded)
		fmt.Fprint(writer, "\n")
	}
	writer.Flush()
}

// record appends one method name to the log, if the test asked for one.
func record(method string) {
	path := os.Getenv(envLog)
	if path == "" {
		return
	}
	handle, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer handle.Close()
	fmt.Fprintln(handle, method)
}

// fakeSpec starts this binary as the named scenario. The scenario travels in the
// environment because Spec.Env is part of what is under test elsewhere, and two
// mechanisms for the same thing would eventually disagree.
func fakeSpec(t *testing.T, scenario string) Spec {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "requests.log")
	t.Setenv(envScenario, scenario)
	t.Setenv(envLog, logPath)
	return Spec{Name: "fake", Command: os.Args[0], Args: []string{"-test.run=^$"}, Timeout: 0}
}

// fakeLog reads the request log the spec wrote to.
func fakeLog(t *testing.T) []string {
	t.Helper()
	path := os.Getenv(envLog)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the request log: %v", err)
	}
	return strings.Fields(string(data))
}

// connectFake opens one fake server and fails the test if it did not connect.
func connectFake(t *testing.T, scenario string) *Set {
	t.Helper()
	set := Open(context.Background(), []Spec{fakeSpec(t, scenario)}, Identity{Name: "atlas", Version: "0.7.0"})
	t.Cleanup(set.Close)
	for _, server := range set.Servers() {
		if server.Err != nil {
			t.Fatalf("server %q: %v", server.Name, server.Err)
		}
	}
	return set
}
