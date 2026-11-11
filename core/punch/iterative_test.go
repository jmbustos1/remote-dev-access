package punch

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/jmbustos1/remote-dev-access/core/natsim"
)

func init() { SweepPause = time.Millisecond }

// Redes de los tests, con IPs de documentación. Las mediciones reales que
// imitan están en docs/experimentos.md.
var (
	// La casa: mismo puerto público hacia todos, y solo entra quien ya recibió algo.
	casa = natsim.NATConfig{IPs: []string{"203.0.113.10"}, Mapping: natsim.EIM, Filtering: natsim.Strict, Preserve: true}
	// El wifi de la oficina (2026-11-11): simétrico, puertos al azar, 2 IPs públicas.
	oficina = natsim.NATConfig{IPs: []string{"198.51.100.20", "198.51.100.21"}, Mapping: natsim.Symmetric}
	// El 4G de la tarde del 2026-11-11: simétrico, con un bloque de 256 puertos.
	cgnatBloque = natsim.NATConfig{IPs: []string{"198.51.100.30"}, Mapping: natsim.Symmetric, PortLo: 33792, PortHi: 34047}
	// El 4G de la mañana: independiente del destino y deja entrar a cualquiera.
	cgnatEIM = natsim.NATConfig{IPs: []string{"198.51.100.40"}, Mapping: natsim.EIM, PortLo: 35328, PortHi: 35839}
)

// simNet arma una internet con servidores STUN repartidos (para que un NAT con
// varias IPs muestre todas) y devuelve cómo abrir una sesión detrás de un NAT.
func simNet(t *testing.T) (open func(nat natsim.NATConfig, ip string) (*Session, chan Event)) {
	t.Helper()
	n := natsim.New(1)
	t.Cleanup(n.Close)
	var servers []string
	for _, ip := range []string{"192.0.2.1", "192.0.2.2", "192.0.2.3", "192.0.2.4", "192.0.2.5", "192.0.2.6"} {
		servers = append(servers, n.STUN(ip))
	}
	return func(nat natsim.NATConfig, ip string) (*Session, chan Event) {
		t.Helper()
		host := n.NAT(nat).Host(ip)
		ch := make(chan Event, 256)
		s, err := Open(context.Background(), Config{
			STUNServers: servers,
			ListenUDP:   func(port uint16) (Conn, error) { return host.ListenUDP(port) },
			OnEvent:     func(e Event) { ch <- e },
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Close)
		return s, ch
	}
}

func never(t *testing.T, ch chan Event, kind EventKind, d time.Duration) {
	t.Helper()
	timeout := time.After(d)
	for {
		select {
		case e := <-ch:
			if e.Kind == kind {
				t.Fatalf("llegó el evento %v y no debía", kind)
			}
		case <-timeout:
			return
		}
	}
}

// Control de la simulación: dos NAT independientes del destino conectan con el
// punching de siempre, y cada lado sabe que no es simétrico.
func TestSimEIMConnectsWithPlainPunching(t *testing.T) {
	open := simNet(t)
	pc, pcEv := open(casa, "192.168.1.10")
	cel, celEv := open(cgnatEIM, "10.0.0.2")
	if pc.NAT() != NATEIM || cel.NAT() != NATEIM {
		t.Fatalf("NAT: casa %q, celular %q; quería eim los dos", pc.NAT(), cel.NAT())
	}
	if pc.Public().Port() != pc.Local().Port() {
		t.Errorf("la casa debería conservar el puerto: %s vs %s", pc.Public(), pc.Local())
	}
	cel.Connect(pc.CandidatesString())
	pc.Connect(cel.CandidatesString())
	waitFor(t, celEv, EvConnected)
	waitFor(t, pcEv, EvConnected)
}

// El caso que motivó 5d: simétrico contra filtrado estricto no conecta con el
// punching normal, en ningún orden (experimentos.md, tarde del 2026-11-11).
func TestSimSymmetricFailsWithPlainPunching(t *testing.T) {
	open := simNet(t)
	pc, pcEv := open(casa, "192.168.1.10")
	mac, macEv := open(oficina, "10.0.0.2")
	if pc.NAT() != NATEIM || mac.NAT() != NATSym {
		t.Fatalf("NAT: casa %q, oficina %q; quería eim y sym", pc.NAT(), mac.NAT())
	}
	mac.Connect(pc.CandidatesString())
	pc.Connect(mac.CandidatesString())
	never(t, macEv, EvConnected, 500*time.Millisecond) // 25 rondas de PING
	never(t, pcEv, EvConnected, 10*time.Millisecond)
}

// iterate corre el camino iterativo: sym abre sockets y eim barre.
func iterate(t *testing.T, sym, eim *Session, sockets int) SweepInfo {
	t.Helper()
	info, err := sym.Spray(eim.CandidatesString(), sockets)
	if err != nil {
		t.Fatal(err)
	}
	if err := eim.Sweep(sym.CandidatesString(), info); err != nil {
		t.Fatal(err)
	}
	return info
}

func TestIterativeConnectsFromOffice(t *testing.T) {
	open := simNet(t)
	pc, pcEv := open(casa, "192.168.1.10")
	mac, macEv := open(oficina, "10.0.0.2")

	info := iterate(t, mac, pc, 256)
	if len(info.IPs) != 2 {
		t.Errorf("IPs anunciadas: %v; quería las 2 de la oficina", info.IPs)
	}
	if info.Lo != 1024 || info.Hi != 65535 {
		t.Errorf("rango %d–%d; con puertos al azar quería todo el rango", info.Lo, info.Hi)
	}
	if want, perIP := info.Probes(); want != perIP || want < 1100 || want > 1200 {
		t.Errorf("disparos por IP: %d de %d; quería ~1161 sin tope", perIP, want)
	}
	waitFor(t, macEv, EvConnected)
	waitFor(t, pcEv, EvConnected)

	// La sesión sigue sirviendo por el socket que acertó, en los dos sentidos.
	if err := mac.Send("hola desde la oficina"); err != nil {
		t.Fatal(err)
	}
	if e := waitFor(t, pcEv, EvMessage); e.Text != "hola desde la oficina" {
		t.Errorf("mensaje = %q", e.Text)
	}
	if err := pc.Send("hola desde la casa"); err != nil {
		t.Fatal(err)
	}
	if e := waitFor(t, macEv, EvMessage); e.Text != "hola desde la casa" {
		t.Errorf("mensaje = %q", e.Text)
	}
	mac.mu.Lock()
	extras := len(mac.extras)
	mac.mu.Unlock()
	if extras != 0 {
		t.Errorf("quedaron %d sockets de Spray abiertos", extras)
	}
}

// Con asignación por bloque el rango es chico: bastan pocos sockets y disparos.
func TestIterativeUsesTheBlock(t *testing.T) {
	open := simNet(t)
	pc, pcEv := open(casa, "192.168.1.10")
	cel, celEv := open(cgnatBloque, "10.0.0.2")

	info := iterate(t, cel, pc, 64)
	if info.Lo > 33792 || info.Hi < 34047 {
		t.Errorf("rango %d–%d no cubre el bloque real 33792–34047", info.Lo, info.Hi)
	}
	if width := int(info.Hi) - int(info.Lo); width > 1024 {
		t.Errorf("rango de %d puertos: debería acotarse al bloque", width)
	}
	if _, perIP := info.Probes(); perIP > 64 {
		t.Errorf("%d disparos por IP: con un bloque chico deberían ser decenas", perIP)
	}
	waitFor(t, celEv, EvConnected)
	waitFor(t, pcEv, EvConnected)
}

// Los roles los define el tipo de NAT, no quién llama: aquí el simétrico es el
// que en producción sería el agente.
func TestIterativeFailsCleanlyWhenNothingMatches(t *testing.T) {
	old := PunchTimeout
	PunchTimeout = 300 * time.Millisecond
	defer func() { PunchTimeout = old }()

	open := simNet(t)
	pc, _ := open(casa, "192.168.1.10")
	mac, macEv := open(oficina, "10.0.0.2")
	// El otro lado nunca barre: Spray tiene que rendirse y cerrar sus sockets.
	if _, err := mac.Spray(pc.CandidatesString(), 32); err != nil {
		t.Fatal(err)
	}
	if e := waitFor(t, macEv, EvFailed); e.Text == "" {
		t.Error("EvFailed sin motivo")
	}
	mac.mu.Lock()
	extras := len(mac.extras)
	mac.mu.Unlock()
	if extras != 0 {
		t.Errorf("quedaron %d sockets de Spray abiertos", extras)
	}
}

// Los datos de barrido vienen del otro lado: nada de red local ni direcciones especiales.
func TestSweepInfoRejectsNonPublicIPs(t *testing.T) {
	base := SweepInfo{Lo: 33792, Hi: 34047, Sockets: 256}
	for ip, ok := range map[string]bool{
		"198.51.100.30": true,
		"192.168.1.11":  false, "10.0.0.1": false, "172.16.5.4": false, // red local
		"127.0.0.1": false, "100.64.1.2": false, "224.0.0.1": false, // loopback, CGNAT, multicast
		"255.255.255.255": false, "0.0.0.0": false, "169.254.1.1": false,
	} {
		info := base
		info.IPs = []netip.Addr{netip.MustParseAddr(ip)}
		if err := info.validate(); (err == nil) != ok {
			t.Errorf("%s: validate() = %v, se esperaba válido=%v", ip, err, ok)
		}
	}
	low := base
	low.IPs, low.Lo = []netip.Addr{netip.MustParseAddr("198.51.100.30")}, 22
	if low.validate() == nil {
		t.Error("aceptó un rango que empieza bajo 1024")
	}
}
