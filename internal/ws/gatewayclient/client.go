package gatewayclient

import (
	"context"
	"log"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"agent-platform/internal/config"
	"agent-platform/internal/retry"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

type Config struct {
	ID      string
	Channel string
	// URL 已由部署侧提供完整入口（含 key/channel 等 query 参数），client 原样使用。
	URL string
	// BaseURL 用于当前反向 gateway 连接发起 request 后的 HTTP 旁路回调。
	BaseURL string
	// Token 是握手使用的 Bearer JWT，来自 channel gateway.jwt-token。
	Token            string
	HandshakeTimeout time.Duration
	ReconnectMin     time.Duration
	ReconnectMax     time.Duration
	OnConnected      func(*ws.Conn)
	OnDisconnected   func(*ws.Conn)
}

type Client struct {
	cfg       Config
	wsCfg     config.WebSocketConfig
	heartbeat time.Duration
	hub       *ws.Hub
	dispatch  ws.RouteHandler

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	curSocket atomic.Pointer[gws.Conn]
	connected atomic.Bool

	startOnce sync.Once
	stopOnce  sync.Once
	rng       *rand.Rand
	rngMu     sync.Mutex
	backoff   retry.BackoffPolicy
}

func New(cfg Config, wsCfg config.WebSocketConfig, heartbeat time.Duration, hub *ws.Hub, dispatch ws.RouteHandler) *Client {
	if cfg.HandshakeTimeout <= 0 {
		cfg.HandshakeTimeout = 10 * time.Second
	}
	if cfg.ReconnectMin <= 0 {
		cfg.ReconnectMin = time.Second
	}
	if cfg.ReconnectMax <= 0 {
		cfg.ReconnectMax = 30 * time.Second
	}
	if cfg.ReconnectMax < cfg.ReconnectMin {
		cfg.ReconnectMax = cfg.ReconnectMin
	}
	return &Client{
		cfg:       cfg,
		wsCfg:     wsCfg,
		heartbeat: heartbeat,
		hub:       hub,
		dispatch:  dispatch,
		done:      make(chan struct{}),
		rng:       rand.New(rand.NewSource(time.Now().UnixNano())),
		backoff: retry.BackoffPolicy{
			Min:         cfg.ReconnectMin,
			Max:         cfg.ReconnectMax,
			Factor:      2,
			JitterRatio: 0.2,
		},
	}
}

func (c *Client) Start(parent context.Context) {
	if c == nil {
		return
	}
	c.startOnce.Do(func() {
		if parent == nil {
			parent = context.Background()
		}
		c.ctx, c.cancel = context.WithCancel(parent)
		log.Printf("gateway websocket client starting: url=%s", c.cfg.URL)
		go c.run()
	})
}

func (c *Client) Stop() error {
	if c == nil {
		return nil
	}
	c.stopOnce.Do(func() {
		started := true
		c.startOnce.Do(func() {
			started = false
		})
		if !started {
			close(c.done)
			return
		}
		if c.cancel != nil {
			c.cancel()
		}
		if sock := c.curSocket.Load(); sock != nil {
			_ = sock.Close()
		}
		<-c.done
	})
	return nil
}

func (c *Client) run() {
	defer close(c.done)
	defer c.connected.Store(false)

	backoff := c.cfg.ReconnectMin
	for {
		if c.ctx.Err() != nil {
			log.Printf("gateway websocket client stopped: url=%s", c.cfg.URL)
			return
		}

		header := http.Header{}
		if strings.TrimSpace(c.cfg.Token) != "" {
			header.Set("Authorization", "Bearer "+c.cfg.Token)
		}
		dialer := &gws.Dialer{HandshakeTimeout: c.cfg.HandshakeTimeout}
		dialCtx, cancel := context.WithTimeout(c.ctx, c.cfg.HandshakeTimeout)
		dialURL := strings.TrimRight(c.cfg.URL, "/")
		socket, resp, err := dialer.DialContext(dialCtx, dialURL, header)
		cancel()
		if err != nil {
			if resp != nil {
				log.Printf("gateway websocket handshake failed: url=%s status=%d err=%v", c.cfg.URL, resp.StatusCode, err)
			} else {
				log.Printf("gateway websocket handshake failed: url=%s err=%v", c.cfg.URL, err)
			}
			delay := c.jitter(backoff)
			log.Printf("gateway websocket reconnect scheduled: url=%s delay=%s", c.cfg.URL, delay.Round(time.Millisecond))
			if !c.sleep(delay) {
				log.Printf("gateway websocket client stopped: url=%s", c.cfg.URL)
				return
			}
			backoff = c.nextBackoff(backoff)
			continue
		}

		c.curSocket.Store(socket)
		c.connected.Store(true)
		if c.ctx.Err() != nil {
			c.connected.Store(false)
			c.curSocket.Store(nil)
			_ = socket.Close()
			log.Printf("gateway websocket client stopped: url=%s", c.cfg.URL)
			return
		}
		connCtx, connCancel := context.WithCancel(c.ctx)
		connCtx = ws.WithGatewayContext(connCtx, ws.GatewayContext{
			ID:      strings.TrimSpace(c.cfg.ID),
			Channel: strings.TrimSpace(c.cfg.Channel),
			BaseURL: strings.TrimSpace(c.cfg.BaseURL),
			Token:   strings.TrimSpace(c.cfg.Token),
		})
		log.Printf("gateway websocket connected: url=%s", c.cfg.URL)
		startedAt := time.Now()
		conn := ws.NewSilentConn(socket, c.hub, c.wsCfg, c.heartbeat, ws.AuthSession{Context: connCtx})
		runDone := make(chan struct{})
		go func() {
			conn.Run(c.dispatch)
			close(runDone)
		}()
		if c.cfg.OnConnected != nil {
			c.cfg.OnConnected(conn)
		}
		<-runDone
		if c.cfg.OnDisconnected != nil {
			c.cfg.OnDisconnected(conn)
		}
		connCancel()
		c.connected.Store(false)
		c.curSocket.Store(nil)
		_ = socket.Close()

		if c.ctx.Err() != nil {
			log.Printf("gateway websocket client stopped: url=%s", c.cfg.URL)
			return
		}

		uptime := time.Since(startedAt)
		log.Printf("gateway websocket disconnected: url=%s uptime=%s", c.cfg.URL, uptime.Round(time.Millisecond))
		if uptime >= c.cfg.ReconnectMax {
			backoff = c.cfg.ReconnectMin
		}
		delay := c.jitter(backoff)
		log.Printf("gateway websocket reconnect scheduled: url=%s delay=%s", c.cfg.URL, delay.Round(time.Millisecond))
		if !c.sleep(delay) {
			log.Printf("gateway websocket client stopped: url=%s", c.cfg.URL)
			return
		}
		backoff = c.nextBackoff(backoff)
	}
}

func (c *Client) Connected() bool {
	if c == nil {
		return false
	}
	return c.connected.Load()
}

func (c *Client) sleep(delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-c.ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (c *Client) nextBackoff(current time.Duration) time.Duration {
	return c.backoff.Next(current)
}

func (c *Client) jitter(base time.Duration) time.Duration {
	c.rngMu.Lock()
	defer c.rngMu.Unlock()
	return c.backoff.Jitter(base, c.rng)
}
