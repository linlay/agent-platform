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

	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/ws"

	gws "github.com/gorilla/websocket"
)

type Config struct {
	// URL 已由部署侧提供完整入口（含 key/channel 等 query 参数），client 原样使用。
	URL string
	// Token 是握手使用的 Bearer JWT，由 GATEWAY_JWT_TOKEN 注入。
	Token            string
	HandshakeTimeout time.Duration
	ReconnectMin     time.Duration
	ReconnectMax     time.Duration
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

	startOnce sync.Once
	stopOnce  sync.Once
	rng       *rand.Rand
	rngMu     sync.Mutex
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
		if c.ctx.Err() != nil {
			c.curSocket.Store(nil)
			_ = socket.Close()
			log.Printf("gateway websocket client stopped: url=%s", c.cfg.URL)
			return
		}
		connCtx, connCancel := context.WithCancel(c.ctx)
		log.Printf("gateway websocket connected: url=%s", c.cfg.URL)
		startedAt := time.Now()
		ws.NewSilentConn(socket, c.hub, c.wsCfg, c.heartbeat, ws.AuthSession{Context: connCtx}).Run(c.dispatch)
		connCancel()
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
	if current <= 0 {
		return c.cfg.ReconnectMin
	}
	next := current * 2
	if next > c.cfg.ReconnectMax {
		return c.cfg.ReconnectMax
	}
	return next
}

func (c *Client) jitter(base time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	delta := int64(base) / 5
	if delta <= 0 {
		return base
	}
	c.rngMu.Lock()
	offset := c.rng.Int63n(2*delta+1) - delta
	c.rngMu.Unlock()
	return time.Duration(int64(base) + offset)
}
