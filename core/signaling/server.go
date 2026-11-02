package signaling

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/jmbustos1/remote-dev-access/core/logx"
)

// Server es el adaptador local: un servidor WebSocket que imita a API Gateway
// (una conexión = un connectionId) y delega toda la lógica en el Hub.
type Server struct {
	hub   *Hub
	mu    sync.Mutex
	conns map[string]*websocket.Conn
}

// NewServer crea un servidor local con el store dado.
func NewServer(store Store) *Server {
	s := &Server{conns: map[string]*websocket.Conn{}}
	s.hub = &Hub{Store: store, Poster: s}
	return s
}

// Post implementa Poster escribiendo en el WebSocket de la conexión.
func (s *Server) Post(ctx context.Context, connID string, msg []byte) error {
	s.mu.Lock()
	c := s.conns[connID]
	s.mu.Unlock()
	if c == nil {
		return ErrGone
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, msg); err != nil {
		return ErrGone
	}
	return nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log := logx.For(logx.StageSignaling)
	device := r.Header.Get(HeaderDevice)
	token := strings.TrimPrefix(r.Header.Get(HeaderAuth), "Bearer ")

	idb := make([]byte, 8)
	rand.Read(idb)
	connID := hex.EncodeToString(idb)

	// Igual que $connect en API Gateway: si falla la autenticación, no hay WebSocket.
	if err := s.hub.Connect(r.Context(), connID, device, token); err != nil {
		log.Warn("conexión rechazada", "device", device, "err", err)
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		s.hub.Disconnect(context.Background(), connID)
		return
	}
	s.mu.Lock()
	s.conns[connID] = c
	s.mu.Unlock()
	log.Info("dispositivo conectado", "device", device, "conn", connID)

	defer func() {
		s.mu.Lock()
		delete(s.conns, connID)
		s.mu.Unlock()
		s.hub.Disconnect(context.Background(), connID)
		c.CloseNow()
		log.Info("dispositivo desconectado", "device", device, "conn", connID)
	}()
	for {
		_, msg, err := c.Read(r.Context())
		if err != nil {
			var ce websocket.CloseError
			if !errors.As(err, &ce) {
				log.Debug("lectura terminada", "device", device, "err", err)
			}
			return
		}
		if err := s.hub.Message(r.Context(), connID, msg); err != nil {
			log.Warn("mensaje con error", "device", device, "err", err)
		}
	}
}
