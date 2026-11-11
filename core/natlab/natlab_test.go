package natlab

import (
	"context"
	"math"
	"math/rand"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/jmbustos1/remote-dev-access/core/stun"
)

var (
	g1  = netip.MustParseAddrPort("74.125.250.129:19302")
	g2  = netip.MustParseAddrPort("74.125.250.129:3478")
	cf  = netip.MustParseAddrPort("162.159.207.0:3478")
	pub = netip.MustParseAddr("203.0.113.7")
)

func obs(dest netip.AddrPort, local, mapped uint16) Obs {
	return Obs{Dest: dest, Local: local, Mapped: netip.AddrPortFrom(pub, mapped)}
}

func TestClassifyMapping(t *testing.T) {
	cases := []struct {
		name string
		obs  []Obs
		want MappingKind
	}{
		{"casa: mismo puerto a todos", []Obs{obs(g1, 5000, 5000), obs(g2, 5000, 5000), obs(cf, 5000, 5000)}, MapIndependent},
		{"cambia solo con otra IP", []Obs{obs(g1, 5000, 40001), obs(g2, 5000, 40001), obs(cf, 5000, 40002)}, MapAddrDep},
		{"cambia con otro puerto", []Obs{obs(g1, 5000, 40001), obs(g2, 5000, 40002), obs(cf, 5000, 40003)}, MapAddrPortDep},
		{"una sola respuesta", []Obs{obs(g1, 5000, 40001), {Dest: cf, Err: net.ErrClosed}}, MapUnknown},
	}
	for _, c := range cases {
		if got := ClassifyMapping(c.obs); got != c.want {
			t.Errorf("%s: %v, quería %v", c.name, got, c.want)
		}
	}
}

func TestPublicIPs(t *testing.T) {
	pub2 := netip.MustParseAddr("203.0.113.8")
	other := func(dest netip.AddrPort, mapped uint16) Obs {
		return Obs{Dest: dest, Local: 5000, Mapped: netip.AddrPortFrom(pub2, mapped)}
	}
	// Los dos puertos de Google salen por la misma IP; Cloudflare por otra.
	in := []Obs{obs(g1, 5000, 40001), obs(g2, 5000, 40002), other(cf, 40003), {Dest: cf, Err: net.ErrClosed}}
	ips := PublicIPs(in)
	if len(ips) != 2 || ips[0] != (IPCount{pub, 2}) || ips[1] != (IPCount{pub2, 1}) {
		t.Errorf("IPs: %v", ips)
	}
	if same, pairs := SameDestSameIP(in); !same || pairs != 1 {
		t.Errorf("por destino: same=%v pairs=%d", same, pairs)
	}
	if same, _ := SameDestSameIP([]Obs{obs(g1, 5000, 40001), other(g2, 40002)}); same {
		t.Error("misma IP destino con IPs públicas distintas debería dar false")
	}

	r := &Result{A: in, Mapping: ClassifyMapping(in), IPs: ips,
		Alloc: AnalyzeAllocation(series(32, func(i int) uint16 { return uint16(50000 + i) },
			func(i int) uint16 { return uint16(1024 + (i*7919)%fullRange) }))}
	txt := r.Text()
	for _, want := range []string{"IPs públicas: 2 en 3 destinos", "se elige por destino", "usa 2 IPs públicas", "~2322 disparos"} {
		if !strings.Contains(txt, want) {
			t.Errorf("falta %q en el reporte:\n%s", want, txt)
		}
	}
	if anon := r.TextAnon(); strings.Contains(anon, "203.0.113.") || !strings.Contains(anon, "PUB1 ×2, PUB2 ×1") {
		t.Errorf("TextAnon dejó IPs o no contó bien:\n%s", anon)
	}
}

func series(n int, local func(i int) uint16, mapped func(i int) uint16) []Obs {
	var out []Obs
	for i := 0; i < n; i++ {
		out = append(out, obs(g1, local(i), mapped(i)))
	}
	return out
}

func TestAnalyzeAllocation(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	local := func(i int) uint16 { return uint16(50000 + i*7) }

	a := AnalyzeAllocation(series(32, local, local))
	if a.Kind != AllocPreserving {
		t.Errorf("conserva: %v", a.Kind)
	}
	a = AnalyzeAllocation(series(32, local, func(i int) uint16 { return uint16(40000 + 2*i) }))
	if a.Kind != AllocSequential || a.MedianAbs != 2 {
		t.Errorf("secuencial: %v mediana %d", a.Kind, a.MedianAbs)
	}
	a = AnalyzeAllocation(series(32, local, func(int) uint16 { return uint16(40960 + r.Intn(2048)) }))
	if a.Kind != AllocBlock || a.Block != 2048 {
		t.Errorf("bloque de 2048: %v bloque %d", a.Kind, a.Block)
	}
	a = AnalyzeAllocation(series(32, local, func(int) uint16 { return uint16(1024 + r.Intn(fullRange)) }))
	if a.Kind != AllocRandom || a.Block != fullRange {
		t.Errorf("al azar: %v bloque %d", a.Kind, a.Block)
	}
}

// Las cifras que usamos en docs/nat-traversal.md.
func TestBirthdayNumbers(t *testing.T) {
	for _, c := range []struct {
		sockets, probes int
		want            float64
	}{{256, 256, 0.64}, {256, 1024, 0.98}, {256, 2048, 0.9997}} {
		if got := HitChance(c.sockets, c.probes, fullRange); math.Abs(got-c.want) > 0.01 {
			t.Errorf("%d×%d: %.4f, quería ~%.4f", c.sockets, c.probes, got, c.want)
		}
	}
	if m := ProbesFor(0.99, 256, fullRange); m < 1100 || m > 1200 {
		t.Errorf("disparos para 99%% con 256 sockets: %d", m)
	}
}

// fakeSTUN responde como un NAT que suma `shift` al puerto (0 = lo conserva).
func fakeSTUN(t *testing.T, shift uint16) int {
	t.Helper()
	srv, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := srv.ReadFromUDPAddrPort(buf)
			if err != nil {
				return
			}
			if stun.IsMessage(buf[:n]) {
				m := netip.AddrPortFrom(from.Addr(), from.Port()+shift)
				srv.WriteToUDPAddrPort(stun.EncodeBindingResponse(stun.TransactionID(buf[:n]), m), from)
			}
		}
	}()
	return srv.LocalAddr().(*net.UDPAddr).Port
}

func TestRunOnLoopback(t *testing.T) {
	p1, p2 := fakeSTUN(t, 0), fakeSTUN(t, 0)
	res, err := Run(context.Background(), Config{
		Targets:  []Target{{"a", "127.0.0.1", p1}, {"b", "127.0.0.1", p2}},
		Sockets:  8,
		Interval: time.Millisecond,
		Label:    "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Mapping != MapIndependent || res.Alloc.Kind != AllocPreserving || res.Alloc.Answers != 8 {
		t.Fatalf("mapeo %v, asignación %v, respuestas %d", res.Mapping, res.Alloc.Kind, res.Alloc.Answers)
	}
	if anon := res.TextAnon(); strings.Contains(anon, "127.0.0.1:") && !strings.Contains(anon, "PUB1") {
		t.Errorf("TextAnon no reemplazó la IP:\n%s", anon)
	}
	txt := res.Text()
	for _, want := range []string{"A. Un socket", "B. 8 sockets", "conserva el puerto local", "No hace falta"} {
		if !strings.Contains(txt, want) {
			t.Errorf("falta %q en el reporte:\n%s", want, txt)
		}
	}
}
