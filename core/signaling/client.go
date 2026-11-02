package signaling

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/jmbustos1/remote-dev-access/core/logx"
)

// PingInterval: API Gateway corta un WebSocket tras 10 min sin tráfico.
var PingInterval = 5 * time.Minute

// Incoming es un mensaje recibido del servidor.
type Incoming = Outbound

// ClientConfig configura un cliente de señalización.
type ClientConfig struct {
	URL    string // wss://… (o ws://… en local)
	Device string
	Token  string
	// OnMessage recibe cada mensaje del servidor (desde la goroutine del cliente).
	OnMessage func(Incoming)
	// OnState recibe "connecting", "connected" o "disconnected: <motivo>".
	OnState func(string)
}

// Client mantiene una conexión de señalización viva y se reconecta solo.
type Client struct {
	cfg    ClientConfig
	ctx    context.Context
	cancel context.CancelFunc

	mu    sync.Mutex
	conn  *websocket.Conn
	ready chan struct{} // se cierra cuando hay conexión
}

// Start arranca el cliente en segundo plano. Close lo detiene.
func Start(cfg ClientConfig) *Client {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{cfg: cfg, ctx: ctx, cancel: cancel, ready: make(chan struct{})}
	go c.run()
	return c
}

// WaitConnected espera hasta que haya conexión (o ctx venza).
func (c *Client) WaitConnected(ctx context.Context) error {
	c.mu.Lock()
	ready := c.ready
	c.mu.Unlock()
	select {
	case <-ready:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("sin conexión con la señalización: %w", ctx.Err())
	}
}

// List pide la lista de dispositivos (la respuesta llega por OnMessage).
func (c *Client) List() error { return c.send(map[string]any{"action": "list"}) }

// Signal envía data al dispositivo to.
func (c *Client) Signal(to string, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return c.send(map[string]any{"action": "signal", "to": to, "data": json.RawMessage(raw)})
}

// Close detiene el cliente.
func (c *Client) Close() { c.cancel() }

func (c *Client) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return errors.New("sin conexión con la señalización")
	}
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	return conn.Write(ctx, websocket.MessageText, b)
}

func (c *Client) state(s string) {
	if c.cfg.OnState != nil {
		c.cfg.OnState(s)
	}
}

func (c *Client) run() {
	log := logx.For(logx.StageSignaling)
	backoff := time.Second
	for c.ctx.Err() == nil {
		c.state("connecting")
		started := time.Now()
		err := c.session()
		if c.ctx.Err() != nil {
			return
		}
		// Si la sesión duró, el corte fue normal (p. ej. el límite de 2 h de API
		// Gateway): reconectar rápido. Si falló al tiro, esperar cada vez más.
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		log.Warn("señalización desconectada; reintentando", "err", err, "en", backoff)
		c.state("disconnected: " + err.Error())
		select {
		case <-time.After(backoff):
		case <-c.ctx.Done():
			return
		}
		backoff = min(backoff*2, time.Minute)
	}
}

// session conecta, lee hasta que se corta y devuelve el motivo.
func (c *Client) session() error {
	log := logx.For(logx.StageSignaling)
	dialCtx, cancel := context.WithTimeout(c.ctx, 15*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(dialCtx, c.cfg.URL, &websocket.DialOptions{
		HTTPHeader: http.Header{
			HeaderDevice: {c.cfg.Device},
			HeaderAuth:   {"Bearer " + c.cfg.Token},
		},
	})
	if err != nil {
		if resp != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
			return fmt.Errorf("rechazado (%d): revisa el nombre y el token del dispositivo", resp.StatusCode)
		}
		return err
	}
	defer conn.CloseNow()

	c.mu.Lock()
	c.conn = conn
	close(c.ready)
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.conn = nil
		c.ready = make(chan struct{})
		c.mu.Unlock()
	}()
	log.Info("conectado a la señalización", "device", c.cfg.Device)
	c.state("connected")

	pingCtx, stopPing := context.WithCancel(c.ctx)
	defer stopPing()
	go func() {
		t := time.NewTicker(PingInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if err := c.send(map[string]any{"action": "ping"}); err != nil {
					log.Debug("ping falló", "err", err)
				}
			case <-pingCtx.Done():
				return
			}
		}
	}()

	for {
		_, msg, err := conn.Read(c.ctx)
		if err != nil {
			return err
		}
		var in Incoming
		if err := json.Unmarshal(msg, &in); err != nil {
			log.Warn("mensaje ilegible de la señalización", "err", err)
			continue
		}
		switch in.Type {
		case "pong":
			log.Debug("pong de la señalización")
		case "error":
			log.Warn("la señalización respondió con error", "error", in.Error)
		}
		if c.cfg.OnMessage != nil {
			c.cfg.OnMessage(in)
		}
	}
}
