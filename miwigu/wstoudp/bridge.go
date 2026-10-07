package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const listenAddr = "127.0.0.1:51820"

// bridge listens for WireGuard UDP on loopback and carries each datagram as
// one WebSocket binary message. Replies take the reverse path, back to the
// last address that sent a datagram.
type bridge struct {
	addr     string
	udp      net.PacketConn
	sendCh   chan []byte
	onStatus func(string)

	mu     sync.Mutex
	url    string
	token  string
	cancel context.CancelFunc
	sess   *wsSession

	peerMu sync.Mutex
	peer   net.Addr
}

type wsSession struct {
	c       *websocket.Conn
	writeMu sync.Mutex
}

func newBridge(addr string, onStatus func(string)) (*bridge, error) {
	pc, err := net.ListenPacket("udp4", addr)
	if err != nil {
		return nil, err
	}
	b := &bridge{
		addr:     addr,
		udp:      pc,
		sendCh:   make(chan []byte, 64),
		onStatus: onStatus,
	}
	go b.udpLoop()
	go b.writer()
	b.onStatus("Listening on " + addr)
	return b, nil
}

// setTarget restarts the WebSocket side. An empty URL disconnects and leaves
// the UDP socket in place, so the WireGuard client can keep using the same endpoint.
func (b *bridge) setTarget(rawURL, rawToken string) {
	rawURL = strings.TrimSpace(rawURL)
	rawToken = strings.TrimSpace(rawToken)

	b.mu.Lock()
	if rawURL == b.url && rawToken == b.token {
		b.mu.Unlock()
		return
	}
	b.url = rawURL
	b.token = rawToken
	if b.cancel != nil {
		b.cancel()
		b.cancel = nil
	}
	old := b.sess
	b.sess = nil
	b.mu.Unlock()
	if old != nil {
		old.c.Close()
	}

	if rawURL == "" {
		b.onStatus("Idle. Listening on " + b.addr)
		return
	}
	if rawToken == "" {
		b.onStatus("Enter an authentication token")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	b.mu.Lock()
	// A newer setTarget won the race while we were closing the old socket.
	if b.url != rawURL || b.token != rawToken {
		b.mu.Unlock()
		cancel()
		return
	}
	b.cancel = cancel
	b.mu.Unlock()

	go b.sessionLoop(ctx, rawURL, rawToken)
}

func (b *bridge) Close() {
	b.mu.Lock()
	if b.cancel != nil {
		b.cancel()
		b.cancel = nil
	}
	sess := b.sess
	b.sess = nil
	b.url = ""
	b.token = ""
	b.mu.Unlock()
	if sess != nil {
		sess.c.Close()
	}
	b.udp.Close()
}

func (b *bridge) sessionLoop(ctx context.Context, url, token string) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		b.onStatus("Connecting")

		dialer := websocket.Dialer{
			HandshakeTimeout: 10 * time.Second,
			Proxy:            http.ProxyFromEnvironment,
			Subprotocols: []string{
				"lambda-microvms",
				"lambda-microvms.authentication." + token,
				"lambda-microvms.port.8080",
			},
		}
		conn, _, err := dialer.DialContext(ctx, url, nil)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			b.onStatus(err.Error())
			if !sleepCtx(ctx, backoff) {
				return
			}
			if backoff < 15*time.Second {
				backoff *= 2
			}
			continue
		}

		backoff = time.Second
		sess := &wsSession{c: conn}
		b.mu.Lock()
		if ctx.Err() != nil || b.url != url || b.token != token {
			b.mu.Unlock()
			conn.Close()
			return
		}
		b.sess = sess
		b.mu.Unlock()
		b.onStatus("Connected")

		err = b.readWS(conn)
		b.mu.Lock()
		if b.sess == sess {
			b.sess = nil
		}
		b.mu.Unlock()
		conn.Close()
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			err = errors.New("connection closed")
		}
		b.onStatus("Disconnected: " + err.Error())
		if !sleepCtx(ctx, time.Second) {
			return
		}
	}
}

func (b *bridge) readWS(conn *websocket.Conn) error {
	conn.SetReadLimit(64 * 1024)
	for {
		kind, msg, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		if kind != websocket.BinaryMessage || len(msg) == 0 {
			continue
		}
		b.peerMu.Lock()
		peer := b.peer
		b.peerMu.Unlock()
		if peer == nil {
			continue
		}
		if _, err := b.udp.WriteTo(msg, peer); err != nil {
			return err
		}
	}
}

func (b *bridge) udpLoop() {
	buf := make([]byte, 64*1024)
	for {
		n, addr, err := b.udp.ReadFrom(buf)
		if err != nil {
			return
		}
		if n == 0 {
			continue
		}
		b.peerMu.Lock()
		b.peer = addr
		b.peerMu.Unlock()

		pkt := make([]byte, n)
		copy(pkt, buf[:n])
		select {
		case b.sendCh <- pkt:
		default:
		}
	}
}

func (b *bridge) writer() {
	for pkt := range b.sendCh {
		b.mu.Lock()
		sess := b.sess
		b.mu.Unlock()
		if sess == nil {
			continue
		}
		sess.writeMu.Lock()
		_ = sess.c.SetWriteDeadline(time.Now().Add(5 * time.Second))
		err := sess.c.WriteMessage(websocket.BinaryMessage, pkt)
		sess.writeMu.Unlock()
		if err != nil {
			sess.c.Close()
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
