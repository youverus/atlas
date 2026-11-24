package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

type cdpRequest struct {
	ID        int64  `json:"id"`
	SessionID string `json:"sessionId,omitempty"`
	Method    string `json:"method"`
	Params    any    `json:"params,omitempty"`
}

type cdpMessage struct {
	ID        int64           `json:"id,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *cdpRPCError    `json:"error,omitempty"`
}

type cdpRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

func (e *cdpRPCError) Error() string {
	if e.Data != "" {
		return fmt.Sprintf("cdp error %d: %s (%s)", e.Code, e.Message, e.Data)
	}
	return fmt.Sprintf("cdp error %d: %s", e.Code, e.Message)
}

type cdpReply struct {
	result json.RawMessage
	err    error
}

// cdpEventHandler receives asynchronous CDP notifications.
type cdpEventHandler func(sessionID, method string, params json.RawMessage)

// cdpClient multiplexes Chrome DevTools Protocol JSON-RPC calls and events over
// one WebSocket connection to the browser target.
type cdpClient struct {
	ws     *wsConn
	nextID atomic.Int64

	mu       sync.Mutex
	pending  map[int64]chan cdpReply
	handlers map[int64]cdpEventHandler
	nextSub  int64
	closed   bool
	done     chan struct{}
	closeErr error
}

func dialCDP(ctx context.Context, wsURL string) (*cdpClient, error) {
	ws, err := dialWebSocket(ctx, wsURL)
	if err != nil {
		return nil, err
	}
	client := &cdpClient{
		ws:       ws,
		pending:  make(map[int64]chan cdpReply),
		handlers: make(map[int64]cdpEventHandler),
		done:     make(chan struct{}),
	}
	go client.readLoop()
	return client, nil
}

// Subscribe registers an event listener and returns a function that unsubscribes it.
func (c *cdpClient) Subscribe(fn cdpEventHandler) func() {
	c.mu.Lock()
	c.nextSub++
	id := c.nextSub
	c.handlers[id] = fn
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		delete(c.handlers, id)
		c.mu.Unlock()
	}
}

// Call executes one CDP method, optionally scoped to a flattened target sessionID.
func (c *cdpClient) Call(ctx context.Context, sessionID, method string, params, out any) error {
	id := c.nextID.Add(1)
	ch := make(chan cdpReply, 1)

	c.mu.Lock()
	if c.closed {
		err := c.closeErr
		c.mu.Unlock()
		if err != nil {
			return err
		}
		return errors.New("browser cdp: connection closed")
	}
	c.pending[id] = ch
	c.mu.Unlock()

	cleanup := func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}

	payload, err := json.Marshal(cdpRequest{
		ID:        id,
		SessionID: sessionID,
		Method:    method,
		Params:    params,
	})
	if err != nil {
		cleanup()
		return err
	}

	if err := c.ws.WriteText(payload); err != nil {
		cleanup()
		return fmt.Errorf("browser cdp: write %s: %w", method, err)
	}

	select {
	case reply := <-ch:
		if reply.err != nil {
			return reply.err
		}
		if out != nil && len(reply.result) > 0 {
			if err := json.Unmarshal(reply.result, out); err != nil {
				return fmt.Errorf("browser cdp: decode %s result: %w", method, err)
			}
		}
		return nil
	case <-c.done:
		cleanup()
		if c.closeErr != nil {
			return c.closeErr
		}
		return errors.New("browser cdp: connection closed")
	case <-ctx.Done():
		cleanup()
		return ctx.Err()
	}
}

func (c *cdpClient) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	return c.ws.Close()
}

func (c *cdpClient) Done() <-chan struct{} { return c.done }

func (c *cdpClient) readLoop() {
	defer close(c.done)
	for {
		raw, err := c.ws.ReadMessage()
		if err != nil {
			c.failAll(err)
			return
		}
		var msg cdpMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		if msg.ID != 0 {
			c.mu.Lock()
			ch := c.pending[msg.ID]
			delete(c.pending, msg.ID)
			c.mu.Unlock()
			if ch != nil {
				var rpcErr error
				if msg.Error != nil {
					rpcErr = msg.Error
				}
				ch <- cdpReply{result: msg.Result, err: rpcErr}
			}
			continue
		}
		if msg.Method != "" {
			c.mu.Lock()
			handlers := make([]cdpEventHandler, 0, len(c.handlers))
			for _, fn := range c.handlers {
				handlers = append(handlers, fn)
			}
			c.mu.Unlock()
			for _, fn := range handlers {
				fn(msg.SessionID, msg.Method, msg.Params)
			}
		}
	}
}

func (c *cdpClient) failAll(err error) {
	c.mu.Lock()
	if c.closeErr == nil {
		c.closeErr = err
	}
	c.closed = true
	pending := c.pending
	c.pending = make(map[int64]chan cdpReply)
	c.mu.Unlock()
	for _, ch := range pending {
		ch <- cdpReply{err: err}
	}
}
