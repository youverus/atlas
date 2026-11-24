// Package browser controls a managed Chromium instance over the Chrome DevTools
// Protocol using only the Go standard library.
package browser

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	wsGUID            = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	maxWSFramePayload = 64 << 20 // 64 MiB cap so large base64 screenshots stay bounded
)

const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA
)

// wsConn is a minimal RFC 6455 client connection used for CDP JSON-RPC.
type wsConn struct {
	conn    net.Conn
	br      *bufio.Reader
	writeMu sync.Mutex
	closeMu sync.Mutex
	closed  bool
}

// dialWebSocket establishes an RFC 6455 WebSocket connection to rawURL.
func dialWebSocket(ctx context.Context, rawURL string) (*wsConn, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("browser ws: invalid url: %w", err)
	}
	var useTLS bool
	switch strings.ToLower(parsed.Scheme) {
	case "ws":
	case "wss":
		useTLS = true
	default:
		return nil, fmt.Errorf("browser ws: unsupported scheme %q", parsed.Scheme)
	}
	host := parsed.Host
	if !strings.Contains(host, ":") {
		if useTLS {
			host += ":443"
		} else {
			host += ":80"
		}
	}

	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var conn net.Conn
	if useTLS {
		conn, err = tls.DialWithDialer(dialer, "tcp", host, &tls.Config{ServerName: parsed.Hostname()})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", host)
	}
	if err != nil {
		return nil, fmt.Errorf("browser ws: dial %s: %w", host, err)
	}

	secKey, err := performWSHandshake(ctx, conn, parsed)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	br := bufio.NewReaderSize(conn, 64*1024)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("browser ws: handshake response: %w", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusSwitchingProtocols {
		_ = conn.Close()
		return nil, fmt.Errorf("browser ws: handshake status %d", resp.StatusCode)
	}
	if got := strings.TrimSpace(resp.Header.Get("Sec-WebSocket-Accept")); got != expectedAccept(secKey) {
		_ = conn.Close()
		return nil, fmt.Errorf("browser ws: invalid Sec-WebSocket-Accept %q", got)
	}

	return &wsConn{conn: conn, br: br}, nil
}

func performWSHandshake(ctx context.Context, conn net.Conn, parsed *url.URL) (string, error) {
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
		defer func() { _ = conn.SetDeadline(time.Time{}) }()
	}
	var keyBytes [16]byte
	if _, err := rand.Read(keyBytes[:]); err != nil {
		return "", fmt.Errorf("browser ws: generate key: %w", err)
	}
	secKey := base64.StdEncoding.EncodeToString(keyBytes[:])
	requestURI := parsed.RequestURI()
	if requestURI == "" {
		requestURI = "/"
	}
	reqText := fmt.Sprintf(
		"GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n",
		requestURI, parsed.Host, secKey,
	)
	if _, err := io.WriteString(conn, reqText); err != nil {
		return "", fmt.Errorf("browser ws: send handshake: %w", err)
	}
	return secKey, nil
}

func expectedAccept(secKey string) string {
	h := sha1.Sum([]byte(secKey + wsGUID))
	return base64.StdEncoding.EncodeToString(h[:])
}

func (c *wsConn) WriteText(payload []byte) error {
	return c.writeFrame(opText, payload)
}

func (c *wsConn) Close() error {
	c.closeMu.Lock()
	if c.closed {
		c.closeMu.Unlock()
		return nil
	}
	c.closed = true
	c.closeMu.Unlock()
	_ = c.writeFrame(opClose, nil)
	return c.conn.Close()
}

func (c *wsConn) writeFrame(opcode byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	var header [14]byte
	header[0] = 0x80 | (opcode & 0x0F)
	n := len(payload)
	idx := 2
	switch {
	case n < 126:
		header[1] = 0x80 | byte(n)
	case n <= 0xFFFF:
		header[1] = 0x80 | 126
		binary.BigEndian.PutUint16(header[2:4], uint16(n))
		idx = 4
	default:
		header[1] = 0x80 | 127
		binary.BigEndian.PutUint64(header[2:10], uint64(n))
		idx = 10
	}

	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return err
	}
	copy(header[idx:idx+4], mask[:])
	idx += 4

	frame := make([]byte, idx+n)
	copy(frame[:idx], header[:idx])
	for i, b := range payload {
		frame[idx+i] = b ^ mask[i%4]
	}

	if _, err := c.conn.Write(frame); err != nil {
		return err
	}
	return nil
}

func (c *wsConn) ReadMessage() ([]byte, error) {
	var assembled []byte
	var messageOp byte
	for {
		fin, opcode, payload, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch opcode {
		case opPing:
			_ = c.writeFrame(opPong, payload)
			continue
		case opPong:
			continue
		case opClose:
			return nil, io.EOF
		case opText, opBinary:
			messageOp = opcode
			assembled = append(assembled[:0], payload...)
		case opContinuation:
			if messageOp == 0 {
				return nil, errors.New("browser ws: unexpected continuation frame")
			}
			if len(assembled)+len(payload) > maxWSFramePayload {
				return nil, errors.New("browser ws: message exceeds size limit")
			}
			assembled = append(assembled, payload...)
		default:
			continue
		}
		if fin {
			return assembled, nil
		}
	}
}

func (c *wsConn) readFrame() (fin bool, opcode byte, payload []byte, err error) {
	var head [2]byte
	if _, err = io.ReadFull(c.br, head[:]); err != nil {
		return false, 0, nil, err
	}
	fin = head[0]&0x80 != 0
	opcode = head[0] & 0x0F
	masked := head[1]&0x80 != 0
	length := uint64(head[1] & 0x7F)

	switch length {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = binary.BigEndian.Uint64(ext[:])
	}
	if length > maxWSFramePayload {
		return false, 0, nil, fmt.Errorf("browser ws: frame too large (%d bytes)", length)
	}

	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(c.br, mask[:]); err != nil {
			return false, 0, nil, err
		}
	}

	payload = make([]byte, int(length))
	if _, err = io.ReadFull(c.br, payload); err != nil {
		return false, 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return fin, opcode, payload, nil
}
