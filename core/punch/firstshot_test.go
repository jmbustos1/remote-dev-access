package punch

import (
	"net"
	"strings"
	"testing"
	"time"
)

// El que espera no manda nada antes de tiempo: así no "envenena" el NAT del
// otro lado (ver ConnectAfter).
func TestConnectAfterHoldsFire(t *testing.T) {
	other, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()

	s, _ := open(t)
	start := time.Now()
	if err := s.ConnectAfter(other.LocalAddr().String(), 300*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1500)
	other.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := other.ReadFromUDP(buf)
	if err != nil {
		t.Fatal("nunca disparó:", err)
	}
	if !strings.HasPrefix(string(buf[:n]), "RDA1 PING") {
		t.Fatalf("primer paquete inesperado: %q", buf[:n])
	}
	if waited := time.Since(start); waited < 250*time.Millisecond {
		t.Errorf("disparó a los %s, antes de la espera", waited)
	}
}

// Si el otro lado dispara primero, el que espera responde al tiro.
func TestConnectAfterAnswersEarlyShot(t *testing.T) {
	waiter, wch := open(t)
	shooter, sch := open(t)

	start := time.Now()
	if err := waiter.ConnectAfter(loopback(shooter), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := shooter.Connect(loopback(waiter)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, sch, EvConnected)
	waitFor(t, wch, EvConnected)
	if took := time.Since(start); took > time.Second {
		t.Errorf("tardó %s: debió dejar de esperar al recibir el primer PING", took)
	}
}
