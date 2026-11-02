package mobile

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jmbustos1/remote-dev-access/core/punch"
	"github.com/jmbustos1/remote-dev-access/core/rendezvous"
	"github.com/jmbustos1/remote-dev-access/core/signaling"
)

// SignalListener lo implementa Kotlin para seguir la señalización.
type SignalListener interface {
	// OnSignalState: "connecting", "connected" o "disconnected: <motivo>".
	OnSignalState(state string)
	// OnDevices: JSON [{"name":"pc-casa","kind":"pc","online":true}, ...]
	OnDevices(devicesJSON string)
}

// SignalClient es la conexión de la app con la señalización (se reconecta sola).
type SignalClient struct {
	d *rendezvous.Dialer
	l SignalListener
}

// StartSignal conecta con la señalización en segundo plano.
func StartSignal(url, device, token string, l SignalListener) *SignalClient {
	c := &SignalClient{l: l}
	c.d = rendezvous.NewDialer(rendezvous.DialerConfig{
		Signaling: signaling.ClientConfig{
			URL: url, Device: device, Token: token,
			OnState: l.OnSignalState,
		},
	})
	return c
}

// RefreshDevices pide la lista de dispositivos; la respuesta llega por OnDevices.
func (c *SignalClient) RefreshDevices() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		devs, err := c.d.Devices(ctx)
		if err != nil {
			return // el estado de la conexión ya se informa por OnSignalState
		}
		if devs == nil {
			devs = []signaling.DeviceStatus{}
		}
		b, _ := json.Marshal(devs)
		c.l.OnDevices(string(b))
	}()
}

// Connect arma la sesión con pc vía señalización + hole punching. Bloquea hasta
// conectar o fallar (~45 s como máximo): llamarlo fuera del hilo de UI.
func (c *SignalClient) Connect(pc string, l PunchListener) (*PunchSession, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s, err := c.d.Connect(ctx, pc, func(e punch.Event) {
		addr := ""
		if e.Addr.IsValid() {
			addr = e.Addr.String()
		}
		l.OnPunchEvent(e.Kind.String(), addr, e.RTT.Milliseconds(), e.Text)
	})
	if err != nil {
		return nil, err
	}
	return &PunchSession{s: s}, nil
}

// Close desconecta de la señalización (no cierra sesiones ya establecidas).
func (c *SignalClient) Close() { c.d.Close() }
