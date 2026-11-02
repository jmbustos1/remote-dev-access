package rendezvous

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jmbustos1/remote-dev-access/core/punch"
	"github.com/jmbustos1/remote-dev-access/core/signaling"
)

func init() {
	punch.PunchInterval = 20 * time.Millisecond
}

// Todo en local: servidor de señalización en memoria, sesiones sin STUN (los
// candidatos son las direcciones locales).
func TestDialerConnectsToListenerAndChats(t *testing.T) {
	store := signaling.NewMemStore(
		signaling.Device{Name: "pc-casa", Kind: signaling.KindPC, TokenHash: signaling.HashToken("tok-pc")},
		signaling.Device{Name: "celular", Kind: signaling.KindPhone, TokenHash: signaling.HashToken("tok-cel")},
	)
	ts := httptest.NewServer(signaling.NewServer(store))
	defer ts.Close()
	url := "ws" + strings.TrimPrefix(ts.URL, "http")

	// El agente responde con eco a cada mensaje.
	l := Listen(ListenerConfig{
		Signaling:   signaling.ClientConfig{URL: url, Device: "pc-casa", Token: "tok-pc"},
		STUNServers: []string{},
		OnEvent: func(from string, s *punch.Session, e punch.Event) {
			if e.Kind == punch.EvMessage && s != nil {
				s.Send("eco: " + e.Text)
			}
		},
	})
	defer l.Close()

	d := NewDialer(DialerConfig{
		Signaling:   signaling.ClientConfig{URL: url, Device: "celular", Token: "tok-cel"},
		STUNServers: []string{},
	})
	defer d.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Esperar a que el PC aparezca en línea.
	for {
		devs, err := d.Devices(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(devs) == 2 && devs[1].Name == "pc-casa" && devs[1].Online {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	msgs := make(chan string, 4)
	s, err := d.Connect(ctx, "pc-casa", func(e punch.Event) {
		if e.Kind == punch.EvMessage {
			msgs <- e.Text
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.Send("hola"); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-msgs:
		if m != "eco: hola" {
			t.Errorf("respuesta = %q", m)
		}
	case <-ctx.Done():
		t.Fatal("no llegó el eco")
	}
}

func TestConnectToOfflinePCFailsFast(t *testing.T) {
	store := signaling.NewMemStore(
		signaling.Device{Name: "pc-casa", Kind: signaling.KindPC, TokenHash: signaling.HashToken("tok-pc")},
		signaling.Device{Name: "celular", Kind: signaling.KindPhone, TokenHash: signaling.HashToken("tok-cel")},
	)
	ts := httptest.NewServer(signaling.NewServer(store))
	defer ts.Close()
	d := NewDialer(DialerConfig{
		Signaling:   signaling.ClientConfig{URL: "ws" + strings.TrimPrefix(ts.URL, "http"), Device: "celular", Token: "tok-cel"},
		STUNServers: []string{},
	})
	defer d.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := d.Connect(ctx, "pc-casa", nil)
	if err == nil || !strings.Contains(err.Error(), "no está conectado") {
		t.Fatalf("err = %v", err)
	}
}
