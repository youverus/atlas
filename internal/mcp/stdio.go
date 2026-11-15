package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"sync"
	"time"
)

// Frame and shutdown budgets.
const (
	// maxMessageBytes bounds one line. A message larger than this is a server
	// answering with something it should have paged; the connection cannot be
	// recovered because the rest of the line is still in the pipe.
	maxMessageBytes = 8 << 20

	// stderrKeep is how much of the server's stderr is kept for diagnosis.
	stderrKeep = 64 << 10

	// gracePeriod is how long a server gets to exit after its stdin closes.
	// A server that ignores EOF is signalled instead.
	gracePeriod = 500 * time.Millisecond
)

// stdioTransport runs one server as a child process and speaks JSON-RPC on its
// stdin and stdout.
//
// Three details are here because each is a failure seen in the wild: the child
// gets its own process group so that a wrapper (npx, uvx, docker) takes the real
// server down with it; stderr is captured instead of forwarded, because the
// reason a server did not start is written there and a terminal interface has
// nowhere to put it; and a line that is not JSON ends the connection rather than
// being skipped, because a server printing to stdout has broken the frame and
// every later reply is suspect.
type stdioTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *os.File
	stderr *tailBuffer

	writeMu sync.Mutex

	mu      sync.Mutex
	pending map[int64]chan incoming
	nextID  int64
	sink    notificationSink

	// closed is the read loop's exit; readErr is why it exited, nil for a clean
	// end of output. waited is cmd.Wait's return.
	closed  chan struct{}
	readErr error
	waited  chan struct{}
	waitErr error

	closeOnce sync.Once
}

// startStdio starts the child and its two pumps.
//
// The pipes are created here rather than through exec's helpers so that Wait and
// the reader never race for the same file: os/exec closes a StdoutPipe inside
// Wait, which would pull the pipe out from under the read loop the moment the
// process exits.
func startStdio(spec Spec) (*stdioTransport, error) {
	command := exec.Command(spec.Command, spec.Args...)
	command.Dir = spec.Dir
	command.Env = mergedEnv(spec.Env)
	confine(command)

	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		stdinRead.Close()
		stdinWrite.Close()
		return nil, err
	}

	stderr := &tailBuffer{limit: stderrKeep}
	command.Stdin = stdinRead
	command.Stdout = stdoutWrite
	command.Stderr = stderr

	if err := command.Start(); err != nil {
		stdinRead.Close()
		stdinWrite.Close()
		stdoutRead.Close()
		stdoutWrite.Close()
		return nil, fmt.Errorf("start %s: %w", spec.Command, err)
	}
	// The child owns the far ends now. The parent must drop its copies or the
	// pipes never report EOF.
	stdinRead.Close()
	stdoutWrite.Close()

	t := &stdioTransport{
		cmd:     command,
		stdin:   stdinWrite,
		stdout:  stdoutRead,
		stderr:  stderr,
		pending: make(map[int64]chan incoming),
		closed:  make(chan struct{}),
		waited:  make(chan struct{}),
	}
	go t.read()
	go func() {
		t.waitErr = command.Wait()
		close(t.waited)
	}()
	return t, nil
}

// read turns bytes into messages. It owns the reader for the life of the
// connection and is the only writer of readErr.
func (t *stdioTransport) read() {
	defer close(t.closed)
	reader := bufio.NewReaderSize(t.stdout, maxMessageBytes)
	for {
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			t.stop(errMessageTooLong)
			return
		}
		if err != nil && !errors.Is(err, io.EOF) {
			t.stop(err)
			return
		}
		if errors.Is(err, io.EOF) {
			// A message ends with a newline, so anything left is the head of a
			// message whose writer died. Reporting the truncation is the point:
			// silently dropping it would look like a server that answered
			// nothing.
			if len(bytes.TrimSpace(line)) > 0 {
				t.stop(fmt.Errorf("%w: %q", errIncompleteMessage, clamp(string(line), 80)))
			} else {
				t.stop(nil)
			}
			return
		}
		if trimmed := bytes.TrimRight(line, "\r\n"); len(trimmed) > 0 {
			if err := t.dispatch(trimmed); err != nil {
				t.stop(err)
				return
			}
		}
	}
}

// dispatch routes one decoded message. An error ends the connection: there is no
// message from a server that can be ignored without guessing.
func (t *stdioTransport) dispatch(line []byte) error {
	var message incoming
	if err := json.Unmarshal(line, &message); err != nil {
		return fmt.Errorf("read from %s: %w: %q", t.cmd.Path, err, clamp(string(line), 80))
	}
	switch {
	case message.isResponse():
		t.deliver(message)
	case message.isRequest():
		// This version declares no capabilities, so a server has nothing to ask
		// that it could act on. Answering -32601 is what the spec asks for, and
		// it is better than leaving the server waiting for a reply.
		t.reply(message.ID, &rpcError{Code: codeMethodNotFound, Message: "method not supported by this client: " + message.Method})
	case message.isNotification():
		// Notifications are the server's own business: logging, progress,
		// resource updates. Which of them is worth acting on is the client's
		// question, so they are handed up rather than dropped here.
		t.deliverNotification(message)
	}
	return nil
}

// listen stores the receiver the read loop hands notifications to. The lock is
// what makes the handoff safe: the loop is already running by the time this is
// called.
func (t *stdioTransport) listen(sink notificationSink) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sink = sink
}

// deliverNotification calls the sink outside the lock: the sink takes its own
// locks, and holding this one would block every pending reply behind it.
func (t *stdioTransport) deliverNotification(message incoming) {
	t.mu.Lock()
	sink := t.sink
	t.mu.Unlock()
	if sink != nil {
		sink(message)
	}
}

// deliver hands a response to the call waiting for it. A missing entry is a
// late answer to a call that already timed out, and dropping it is the only
// correct handling.
func (t *stdioTransport) deliver(message incoming) {
	t.mu.Lock()
	answers := t.pending[*message.ID]
	t.mu.Unlock()
	if answers == nil {
		return
	}
	select {
	case answers <- message:
	default:
	}
}

// reply answers a server-initiated request. Failures are dropped: the write
// either worked or the connection is already gone.
func (t *stdioTransport) reply(id *int64, failure *rpcError) {
	message := rpcResponse{JSONRPC: jsonrpcVersion, ID: id, Error: failure}
	encoded, err := json.Marshal(message)
	if err != nil {
		return
	}
	_ = t.writeLine(encoded)
}

// stop records why the read loop ended and wakes every waiting call.
func (t *stdioTransport) stop(err error) {
	t.mu.Lock()
	t.readErr = err
	t.mu.Unlock()
}

func (t *stdioTransport) newID() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nextID++
	return t.nextID
}

func (t *stdioTransport) expect(id int64) chan incoming {
	answers := make(chan incoming, 1)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pending[id] = answers
	return answers
}

func (t *stdioTransport) forget(id int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.pending, id)
}

// Call sends one request and waits for its answer.
func (t *stdioTransport) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	payload, err := describeParams(params)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	id := t.newID()
	answers := t.expect(id)
	defer t.forget(id)

	if err := t.write(rpcRequest{JSONRPC: jsonrpcVersion, ID: id, Method: method, Params: payload}); err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	select {
	case message := <-answers:
		if message.Error != nil {
			return nil, fmt.Errorf("%s: %w", method, message.Error)
		}
		return message.Result, nil
	case <-ctx.Done():
		t.cancelled(id, ctx.Err())
		return nil, fmt.Errorf("%s: %w", method, ctx.Err())
	case <-t.closed:
		return nil, fmt.Errorf("%s: %w", method, t.failure())
	}
}

// Notify sends a message that expects no answer.
func (t *stdioTransport) Notify(ctx context.Context, method string, params any) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	payload, err := describeParams(params)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	if err := t.write(rpcRequest{JSONRPC: jsonrpcVersion, Method: method, Params: payload}); err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	return nil
}

// write frames one message under the write lock. Two tool calls may be in flight
// at once, and a half-written line would corrupt both.
func (t *stdioTransport) write(message rpcRequest) error {
	encoded, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode %s: %w", message.Method, err)
	}
	return t.writeLine(encoded)
}

func (t *stdioTransport) writeLine(encoded []byte) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if _, err := t.stdin.Write(append(encoded, '\n')); err != nil {
		return err
	}
	return nil
}

// cancelled tells the server to stop working on a request whose answer nobody
// wants any more. It runs in the background because the caller is on its way out
// with the timeout, and a slow goodbye must not become a second wait.
func (t *stdioTransport) cancelled(id int64, reason error) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = t.Notify(ctx, "notifications/cancelled", map[string]any{"requestId": id, "reason": reason.Error()})
	}()
}

// failure explains why the connection ended, quoting the server's stderr when
// there is any: "connection closed" alone is the least useful error there is.
func (t *stdioTransport) failure() error {
	t.mu.Lock()
	readErr := t.readErr
	t.mu.Unlock()
	if readErr == nil {
		readErr = errors.New("the server closed its output")
	}
	tail := quoteForError(t.stderr.String())
	if tail == "" {
		return readErr
	}
	return fmt.Errorf("%w: %s", readErr, tail)
}

// Stderr returns what the server wrote to stderr, for the caller that reports a
// failed connection.
func (t *stdioTransport) Stderr() string { return quoteForError(t.stderr.String()) }

// Close shuts the server down: stdin first, so a well-behaved server can finish
// what it was doing, then the whole process group for one that ignores EOF.
func (t *stdioTransport) Close() error {
	t.closeOnce.Do(func() {
		_ = t.stdin.Close()
		if !waitClosed(t.waited, gracePeriod) {
			killGroup(t.cmd)
			<-t.waited
		}
		_ = t.stdout.Close()
	})
	return nil
}

// mergedEnv is the process environment plus the configured variables. Inheriting
// is the default because a server needs PATH to find anything at all; the
// configured names are sorted so two runs pass an identical environment.
func mergedEnv(extra map[string]string) []string {
	if len(extra) == 0 {
		return nil
	}
	env := os.Environ()
	names := make([]string, 0, len(extra))
	for name := range extra {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		env = append(env, name+"="+extra[name])
	}
	return env
}

// clamp shortens a value for an error message.
func clamp(text string, max int) string {
	if len(text) <= max {
		return text
	}
	return text[:max] + "…"
}
