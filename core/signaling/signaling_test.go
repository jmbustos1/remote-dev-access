package signaling

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testServer(t *testing.T) string {
	t.Helper()
	store := NewMemStore(
		Device{Name: "pc-casa", Kind: KindPC, TokenHash: HashToken("tok-pc")},
		Device{Name: "celular", Kind: KindPhone, TokenHash: HashToken("tok-cel")},
	)
	ts := httptest.NewServer(NewServer(store))
	t.Cleanup(ts.Close)
	return "ws" + strings.TrimPrefix(ts.URL, "http")
}

func client(t *testing.T, url, device, token string) (*Client, chan Incoming, chan string) {
	t.Helper()
	msgs := make(chan Incoming, 16)
	states := make(chan string, 16)
	c := Start(ClientConfig{URL: url, Device: device, Token: token,
		OnMessage: func(m Incoming) { msgs <- m },
		OnState:   func(s string) { states <- s },
	})
	t.Cleanup(c.Close)
	return c, msgs, states
}

func next(t *testing.T, ch chan Incoming, typ string) Incoming {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case m := <-ch:
			if m.Type == typ {
				return m
			}
		case <-timeout:
			t.Fatalf("no llegó mensaje %q", typ)
		}
	}
}

func TestListAndSignal(t *testing.T) {
	url := testServer(t)
	pc, pcMsgs, _ := client(t, url, "pc-casa", "tok-pc")
	cel, celMsgs, _ := client(t, url, "celular", "tok-cel")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := pc.WaitConnected(ctx); err != nil {
		t.Fatal(err)
	}
	if err := cel.WaitConnected(ctx); err != nil {
		t.Fatal(err)
	}

	cel.List()
	devs := next(t, celMsgs, "devices").Devices
	if len(devs) != 2 || devs[1].Name != "pc-casa" || !devs[1].Online {
		t.Errorf("devices = %+v", devs)
	}

	cel.Signal("pc-casa", map[string]string{"kind": "offer", "candidates": "1.2.3.4:5"})
	m := next(t, pcMsgs, "signal")
	var data map[string]string
	json.Unmarshal(m.Data, &data)
	if m.From != "celular" || data["candidates"] != "1.2.3.4:5" {
		t.Errorf("signal = %+v %v", m, data)
	}
}

func TestSignalToOfflineDevice(t *testing.T) {
	url := testServer(t)
	cel, msgs, _ := client(t, url, "celular", "tok-cel")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cel.WaitConnected(ctx)
	cel.Signal("pc-casa", map[string]string{"kind": "offer"})
	if e := next(t, msgs, "error"); !strings.Contains(e.Error, "no está conectado") {
		t.Errorf("error = %q", e.Error)
	}
}

func TestBadTokenIsRejected(t *testing.T) {
	url := testServer(t)
	_, _, states := client(t, url, "pc-casa", "mal")
	timeout := time.After(3 * time.Second)
	for {
		select {
		case s := <-states:
			if strings.Contains(s, "rechazado") {
				return
			}
			if s == "connected" {
				t.Fatal("conectó con token inválido")
			}
		case <-timeout:
			t.Fatal("no informó el rechazo")
		}
	}
}

func TestPingPong(t *testing.T) {
	old := PingInterval
	PingInterval = 50 * time.Millisecond
	defer func() { PingInterval = old }()
	url := testServer(t)
	c, msgs, _ := client(t, url, "pc-casa", "tok-pc")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c.WaitConnected(ctx)
	next(t, msgs, "pong")
}
