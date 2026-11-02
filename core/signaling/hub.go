// Package signaling es la "guía telefónica": sabe qué dispositivos están
// conectados y reenvía mensajes chicos entre ellos (los candidatos para el
// hole punching). No transporta datos de la sesión: eso va directo P2P.
//
// La lógica (Hub) no sabe dónde corre. Dos adaptadores la usan:
//   - local:  servidor WebSocket en memoria (Server), para desarrollo y tests.
//   - Lambda: API Gateway WebSocket + DynamoDB (signaling/lambda).
//
// Protocolo (JSON sobre WebSocket):
//
//	cliente → servidor  {"action":"list"}
//	                    {"action":"signal","to":"pc-casa","data":{...}}
//	                    {"action":"ping"}
//	servidor → cliente  {"type":"devices","devices":[{"name":"pc-casa","kind":"pc","online":true}]}
//	                    {"type":"signal","from":"celular","data":{...}}
//	                    {"type":"pong"}
//	                    {"type":"error","error":"..."}
//
// Autenticación al conectar: headers "X-RDA-Device: <nombre>" y
// "Authorization: Bearer <token>". El servidor guarda solo el SHA-256 del token.
package signaling

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// Tipos de dispositivo.
const (
	KindPC    = "pc"
	KindPhone = "phone"
)

// Headers de autenticación.
const (
	HeaderDevice = "X-RDA-Device"
	HeaderAuth   = "Authorization"
)

var (
	ErrUnauthorized = errors.New("dispositivo o token inválido")
	ErrNotFound     = errors.New("no existe")
	ErrGone         = errors.New("la conexión ya no existe")
)

// Device es un dispositivo registrado.
type Device struct {
	Name      string
	Kind      string
	TokenHash string // hex(sha256(token))
}

// DeviceStatus es lo que ve un cliente al pedir la lista.
type DeviceStatus struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Online bool   `json:"online"`
}

// Store guarda dispositivos y conexiones (memoria en local, DynamoDB en AWS).
type Store interface {
	Device(ctx context.Context, name string) (Device, error)
	Devices(ctx context.Context) ([]DeviceStatus, error)
	SetConnection(ctx context.Context, device, connID string) error
	// ClearConnection borra la conexión y devuelve a qué dispositivo pertenecía.
	ClearConnection(ctx context.Context, connID string) (string, error)
	ConnectionOf(ctx context.Context, device string) (string, error)
	DeviceOf(ctx context.Context, connID string) (string, error)
}

// Poster envía un mensaje a una conexión abierta (ErrGone si ya no existe).
type Poster interface {
	Post(ctx context.Context, connID string, msg []byte) error
}

// Hub es la lógica de señalización.
type Hub struct {
	Store  Store
	Poster Poster
}

// HashToken es lo que se guarda en vez del token.
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// Connect autentica una conexión nueva y la asocia al dispositivo.
func (h *Hub) Connect(ctx context.Context, connID, device, token string) error {
	d, err := h.Store.Device(ctx, device)
	if err != nil {
		return ErrUnauthorized
	}
	got := HashToken(token)
	if subtle.ConstantTimeCompare([]byte(got), []byte(d.TokenHash)) != 1 {
		return ErrUnauthorized
	}
	return h.Store.SetConnection(ctx, device, connID)
}

// Disconnect olvida la conexión.
func (h *Hub) Disconnect(ctx context.Context, connID string) error {
	_, err := h.Store.ClearConnection(ctx, connID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

type inbound struct {
	Action string          `json:"action"`
	To     string          `json:"to,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// Outbound es cualquier mensaje del servidor al cliente.
type Outbound struct {
	Type    string          `json:"type"`
	From    string          `json:"from,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
	Devices []DeviceStatus  `json:"devices,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// Message procesa un mensaje de una conexión ya autenticada.
func (h *Hub) Message(ctx context.Context, connID string, body []byte) error {
	from, err := h.Store.DeviceOf(ctx, connID)
	if err != nil {
		return ErrUnauthorized
	}
	var in inbound
	if err := json.Unmarshal(body, &in); err != nil {
		return h.reply(ctx, connID, Outbound{Type: "error", Error: "JSON inválido"})
	}
	switch in.Action {
	case "ping":
		return h.reply(ctx, connID, Outbound{Type: "pong"})
	case "list":
		devs, err := h.Store.Devices(ctx)
		if err != nil {
			return err
		}
		sort.Slice(devs, func(i, j int) bool { return devs[i].Name < devs[j].Name })
		return h.reply(ctx, connID, Outbound{Type: "devices", Devices: devs})
	case "signal":
		if in.To == "" || len(in.Data) == 0 {
			return h.reply(ctx, connID, Outbound{Type: "error", Error: "signal requiere to y data"})
		}
		if len(in.Data) > 4096 {
			return h.reply(ctx, connID, Outbound{Type: "error", Error: "data demasiado grande"})
		}
		target, err := h.Store.ConnectionOf(ctx, in.To)
		if err != nil {
			return h.reply(ctx, connID, Outbound{Type: "error", Error: in.To + " no está conectado"})
		}
		msg, _ := json.Marshal(Outbound{Type: "signal", From: from, Data: in.Data})
		if err := h.Poster.Post(ctx, target, msg); err != nil {
			if errors.Is(err, ErrGone) {
				h.Store.ClearConnection(ctx, target)
				return h.reply(ctx, connID, Outbound{Type: "error", Error: in.To + " no está conectado"})
			}
			return err
		}
		return nil
	}
	return h.reply(ctx, connID, Outbound{Type: "error", Error: fmt.Sprintf("acción desconocida %q", in.Action)})
}

func (h *Hub) reply(ctx context.Context, connID string, o Outbound) error {
	b, _ := json.Marshal(o)
	return h.Poster.Post(ctx, connID, b)
}
