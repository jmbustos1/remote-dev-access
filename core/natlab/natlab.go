// Package natlab mide cómo se porta el NAT de la red actual, para decidir si la
// camino iterativo (barrer puertos: paradoja del cumpleaños) puede funcionar antes de
// gastar el relay. Solo usa servidores STUN públicos: no hace falta el PC ni
// tocar el router. Ver docs/nat-traversal.md.
//
// Mide dos cosas:
//
//	A. Un socket, varios destinos → ¿el puerto público cambia según el destino?
//	   (independiente / depende de la IP / depende de IP y puerto) y ¿cuántas
//	   IPs públicas distintas aparecen?
//	B. Muchos sockets, un destino → ¿cómo reparte puertos el NAT entre sockets?
//	   (conserva el puerto / secuencial / por bloque / al azar) y ¿siempre la misma IP?
package natlab

import (
	"context"
	"fmt"
	"math"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/jmbustos1/remote-dev-access/core/netx"
	"github.com/jmbustos1/remote-dev-access/core/stun"
)

// Target es un servidor STUN al que se le pregunta. Host se resuelve una sola
// vez, así dos Target con el mismo Host usan la MISMA IP (y solo cambia el
// puerto): eso es lo que permite distinguir "depende de la IP" de "depende del
// puerto".
type Target struct {
	Name string
	Host string
	Port int
}

// DefaultTargets: pares de puertos en la misma IP (Google, Nextcloud) y una
// veintena de operadores en redes distintas. Con muchos destinos se ve, además
// del mapeo, cuántas IPs públicas usa el NAT: un CGNAT puede elegir la IP según
// el destino, y con 5 destinos no se distingue un pool de 2 de uno de 4.
// Todos respondían el 2026-11-11; uno caído solo cuesta ~3 s y no invalida nada.
var DefaultTargets = []Target{
	{"google", "stun.l.google.com", 19302},
	{"google", "stun.l.google.com", 3478},
	{"cloudflare", "stun.cloudflare.com", 3478},
	{"twilio", "global.stun.twilio.com", 3478},
	{"nextcloud", "stun.nextcloud.com", 3478},
	{"nextcloud", "stun.nextcloud.com", 443},
	{"sipgate", "stun.sipgate.net", 3478},
	{"blackberry", "stun.voip.blackberry.com", 3478},
	{"schlund", "stun.schlund.de", 3478},
	{"voipgate", "stun.voipgate.com", 3478},
	{"antisip", "stun.antisip.com", 3478},
	{"dus", "stun.dus.net", 3478},
	{"epygi", "stun.epygi.com", 3478},
	{"sonetel", "stun.sonetel.com", 3478},
	{"voys", "stun.voys.nl", 3478},
	{"freeswitch", "stun.freeswitch.org", 3478},
	{"framasoft", "stun.framasoft.org", 3478},
	{"telnyx", "stun.telnyx.com", 3478},
	{"linphone", "stun.linphone.org", 3478},
	{"hot-chilli", "stun.hot-chilli.net", 3478},
	{"siptrunk", "stun.siptrunk.com", 3478},
	{"acronis", "stun.acronis.com", 3478},
}

// Config del experimento.
type Config struct {
	Targets  []Target      // parte A (el primero también se usa en la parte B)
	Sockets  int           // parte B: cuántos sockets abrir (por defecto 32)
	Interval time.Duration // parte B: pausa entre sockets (por defecto 100 ms)
	Timeout  time.Duration // por consulta STUN (por defecto 1,5 s)
	Label    string        // descripción de la red ("datos móviles", "wifi casa"…)
}

// Obs es una respuesta STUN: desde qué puerto local, a qué destino, qué vio internet.
type Obs struct {
	Target string         // "google 74.125.250.129:19302"
	Dest   netip.AddrPort // destino resuelto
	Local  uint16
	Mapped netip.AddrPort
	At     time.Duration // desde el inicio de la parte
	Err    error
}

// Result del experimento completo.
type Result struct {
	Label   string
	Started time.Time
	Local   netip.AddrPort // IP local del equipo (para saber si hay NAT)
	A       []Obs
	B       []Obs
	Mapping MappingKind
	IPs     []IPCount // IPs públicas vistas en la parte A y hacia cuántos destinos
	Alloc   Allocation
}

// Run ejecuta las partes A y B. Bloquea unos segundos (Sockets × Interval).
func Run(ctx context.Context, cfg Config) (*Result, error) {
	if len(cfg.Targets) == 0 {
		cfg.Targets = DefaultTargets
	}
	if cfg.Sockets <= 0 {
		cfg.Sockets = 32
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 100 * time.Millisecond
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 1500 * time.Millisecond
	}

	dests, names, err := resolve(ctx, cfg.Targets)
	if err != nil {
		return nil, err
	}
	res := &Result{Label: cfg.Label, Started: time.Now()}

	// A. Un socket, todos los destinos, en orden.
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil, fmt.Errorf("abriendo socket UDP: %w", err)
	}
	local := uint16(conn.LocalAddr().(*net.UDPAddr).Port)
	res.Local = netx.LocalAddr(local)
	t0 := time.Now()
	for i, d := range dests {
		res.A = append(res.A, query(conn, local, names[i], d, cfg.Timeout, t0))
	}
	conn.Close()
	res.Mapping = ClassifyMapping(res.A)
	res.IPs = PublicIPs(res.A)

	// B. Un socket nuevo cada Interval, todos al primer destino. Los sockets
	// quedan abiertos hasta el final para que el NAT no recicle puertos.
	var open []*net.UDPConn
	defer func() {
		for _, c := range open {
			c.Close()
		}
	}()
	t0 = time.Now()
	for i := 0; i < cfg.Sockets; i++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		c, err := net.ListenUDP("udp4", &net.UDPAddr{})
		if err != nil {
			res.B = append(res.B, Obs{Target: names[0], Err: err, At: time.Since(t0)})
			continue
		}
		open = append(open, c)
		res.B = append(res.B, query(c, uint16(c.LocalAddr().(*net.UDPAddr).Port), names[0], dests[0], cfg.Timeout, t0))
		time.Sleep(cfg.Interval)
	}
	res.Alloc = AnalyzeAllocation(res.B)
	return res, nil
}

func resolve(ctx context.Context, ts []Target) ([]netip.AddrPort, []string, error) {
	ips := map[string]netip.Addr{}
	var dests []netip.AddrPort
	var names []string
	for _, t := range ts {
		ip, ok := ips[t.Host]
		if !ok {
			if a, err := netip.ParseAddr(t.Host); err == nil {
				ip = a
			} else {
				ua, err := netx.ResolveUDP4(ctx, net.JoinHostPort(t.Host, "0"))
				if err != nil {
					continue // un servidor caído no invalida el experimento
				}
				ip = ua.AddrPort().Addr().Unmap()
			}
			ips[t.Host] = ip
		}
		d := netip.AddrPortFrom(ip, uint16(t.Port))
		if slices.Contains(dests, d) {
			continue // dos nombres que resuelven al mismo servidor: no aporta
		}
		dests = append(dests, d)
		names = append(names, t.Name+" "+d.String())
	}
	if len(dests) == 0 {
		return nil, nil, fmt.Errorf("no se pudo resolver ningún servidor STUN")
	}
	return dests, names, nil
}

func query(c *net.UDPConn, local uint16, name string, d netip.AddrPort, timeout time.Duration, t0 time.Time) Obs {
	o := Obs{Target: name, Dest: d, Local: local, At: time.Since(t0)}
	o.Mapped, _, o.Err = stun.Query(c, net.UDPAddrFromAddrPort(d), timeout, 2)
	return o
}

// ---------------------------------------------------------------------------
// Análisis (funciones puras: se testean con datos sintéticos)

// MappingKind clasifica el comportamiento de mapeo (RFC 4787).
type MappingKind int

const (
	MapUnknown     MappingKind = iota
	MapIndependent             // mismo IP:puerto público hacia cualquier destino
	MapAddrDep                 // cambia al cambiar la IP destino, no el puerto
	MapAddrPortDep             // cambia incluso con la misma IP y otro puerto
)

func (k MappingKind) String() string {
	switch k {
	case MapIndependent:
		return "independiente del destino (el punching normal funciona)"
	case MapAddrDep:
		return "depende de la IP destino (simétrico \"suave\")"
	case MapAddrPortDep:
		return "depende de IP y puerto destino (simétrico)"
	}
	return "desconocido (faltan respuestas)"
}

// ClassifyMapping compara los mapeos de un mismo socket hacia distintos destinos.
func ClassifyMapping(obs []Obs) MappingKind {
	var ok []Obs
	for _, o := range obs {
		if o.Err == nil {
			ok = append(ok, o)
		}
	}
	if len(ok) < 2 {
		return MapUnknown
	}
	sameIPDiffPort, diffIP := false, false // ¿cambió el mapeo en cada situación?
	sawSameIP, sawDiffIP := false, false
	for i := 0; i < len(ok); i++ {
		for j := i + 1; j < len(ok); j++ {
			changed := ok[i].Mapped != ok[j].Mapped
			if ok[i].Dest.Addr() == ok[j].Dest.Addr() {
				sawSameIP = true
				sameIPDiffPort = sameIPDiffPort || changed
			} else {
				sawDiffIP = true
				diffIP = diffIP || changed
			}
		}
	}
	switch {
	case sameIPDiffPort:
		return MapAddrPortDep
	case diffIP && sawSameIP:
		return MapAddrDep
	case diffIP:
		return MapAddrPortDep // sin par "misma IP" no se puede afinar: lo peor
	case sawDiffIP || sawSameIP:
		return MapIndependent
	}
	return MapUnknown
}

// IPCount: una IP pública y cuántas respuestas la mostraron.
type IPCount struct {
	IP netip.Addr
	N  int
}

// PublicIPs cuenta las IPs públicas distintas, en orden de aparición. Más de
// una en la parte A = el NAT elige la IP según el destino: el otro lado no
// sabe por cuál vamos a salir hacia él.
func PublicIPs(obs []Obs) []IPCount {
	var out []IPCount
	for _, o := range obs {
		if o.Err != nil {
			continue
		}
		ip := o.Mapped.Addr()
		i := slices.IndexFunc(out, func(c IPCount) bool { return c.IP == ip })
		if i < 0 {
			out = append(out, IPCount{IP: ip})
			i = len(out) - 1
		}
		out[i].N++
	}
	return out
}

// SameDestSameIP mira los destinos que comparten IP (otro puerto): ¿salimos
// hacia ellos por la misma IP pública? pairs es cuántos pares se compararon.
func SameDestSameIP(obs []Obs) (same bool, pairs int) {
	same = true
	for i := range obs {
		for j := i + 1; j < len(obs); j++ {
			if obs[i].Err != nil || obs[j].Err != nil || obs[i].Dest.Addr() != obs[j].Dest.Addr() {
				continue
			}
			pairs++
			same = same && obs[i].Mapped.Addr() == obs[j].Mapped.Addr()
		}
	}
	return same, pairs
}

// AllocKind: cómo elige el NAT el puerto público de cada socket nuevo.
type AllocKind int

const (
	AllocUnknown    AllocKind = iota
	AllocPreserving           // puerto público = puerto local
	AllocSequential           // cada socket nuevo recibe un puerto cercano al anterior
	AllocBlock                // al azar, pero dentro de un rango chico (bloque por cliente)
	AllocRandom               // al azar en todo el rango
)

func (k AllocKind) String() string {
	return [...]string{"desconocida", "conserva el puerto local", "secuencial", "por bloque", "al azar"}[k]
}

// Allocation resume la parte B.
type Allocation struct {
	Kind      AllocKind
	Answers   int // respuestas recibidas
	PublicIPs []string
	Preserved int    // cuántos conservaron el puerto local
	Deltas    []int  // saltos de puerto entre sockets consecutivos (en orden de apertura)
	MedianAbs int    // mediana de |salto|
	Min, Max  uint16 // rango de puertos públicos vistos
	Block     int    // tamaño estimado del rango efectivo (para el camino iterativo)
}

// Rango de puertos que un NAT suele usar al azar (1024–65535).
const fullRange = 65536 - 1024

// AnalyzeAllocation clasifica cómo el NAT reparte puertos entre sockets.
func AnalyzeAllocation(obs []Obs) Allocation {
	var a Allocation
	var ports []uint16
	ips := map[string]bool{}
	for _, o := range obs {
		if o.Err != nil {
			continue
		}
		a.Answers++
		ip := o.Mapped.Addr().String()
		if !ips[ip] {
			ips[ip] = true
			a.PublicIPs = append(a.PublicIPs, ip)
		}
		if o.Mapped.Port() == o.Local {
			a.Preserved++
		}
		ports = append(ports, o.Mapped.Port())
	}
	if a.Answers < 4 {
		return a
	}
	abs := make([]int, 0, len(ports)-1)
	for i := 1; i < len(ports); i++ {
		d := int(ports[i]) - int(ports[i-1])
		a.Deltas = append(a.Deltas, d)
		if d < 0 {
			d = -d
		}
		abs = append(abs, d)
	}
	slices.Sort(abs)
	a.MedianAbs = abs[len(abs)/2]
	a.Min, a.Max = slices.Min(ports), slices.Max(ports)

	// Rango esperado de N puertos al azar en todo el espacio: ≈ R·(N−1)/(N+1).
	n := float64(len(ports))
	expected := fullRange * (n - 1) / (n + 1)
	spread := float64(a.Max - a.Min)
	switch {
	case a.Preserved*10 >= a.Answers*8: // ≥ 80 %
		a.Kind = AllocPreserving
		a.Block = 1
	case a.MedianAbs <= 16:
		a.Kind = AllocSequential
		a.Block = 64 // basta probar una ventana chica alrededor del último puerto visto
	case spread < expected/4:
		a.Kind = AllocBlock
		// El rango observado subestima el bloque real: lo corregimos y
		// redondeamos a la potencia de 2 más cercana (los CGNAT reparten bloques así).
		est := spread * (n + 1) / (n - 1)
		a.Block = 1 << int(math.Round(math.Log2(math.Max(est, 2))))
	default:
		a.Kind = AllocRandom
		a.Block = fullRange
	}
	return a
}

// ProbesFor dice cuántos puertos tiene que probar el otro lado para acertar con
// probabilidad p, si este lado abre `sockets` sockets y el NAT los reparte en un
// rango de `block` puertos: p = 1 − e^(−sockets·probes/block).
func ProbesFor(p float64, sockets, block int) int {
	if block <= 1 {
		return 1
	}
	m := -math.Log(1-p) * float64(block) / float64(sockets)
	return int(math.Min(math.Ceil(m), float64(block)))
}

// HitChance es la probabilidad de al menos una coincidencia.
func HitChance(sockets, probes, block int) float64 {
	if block <= 1 {
		return 1
	}
	return 1 - math.Exp(-float64(sockets)*float64(probes)/float64(block))
}

// ---------------------------------------------------------------------------
// Reporte

// Text arma el reporte para leer en pantalla y pegar en docs/experimentos.md.
func (r *Result) Text() string {
	var b strings.Builder
	label := r.Label
	if label == "" {
		label = "red sin nombre"
	}
	fmt.Fprintf(&b, "Experimento NAT — %s — %s\n", label, r.Started.Format("2006-01-02 15:04"))
	fmt.Fprintf(&b, "IP local: %s\n\n", r.Local.Addr())

	b.WriteString("A. Un socket, varios destinos\n")
	for _, o := range r.A {
		writeObs(&b, o)
	}
	fmt.Fprintf(&b, "  → mapeo: %s\n", r.Mapping)
	if len(r.IPs) > 0 {
		answers := 0
		var parts []string
		for _, c := range r.IPs {
			answers += c.N
			parts = append(parts, fmt.Sprintf("%s ×%d", c.IP, c.N))
		}
		fmt.Fprintf(&b, "  → IPs públicas: %d en %d destinos (%s)\n", len(r.IPs), answers, strings.Join(parts, ", "))
		if same, pairs := SameDestSameIP(r.A); len(r.IPs) > 1 && pairs > 0 {
			if same {
				fmt.Fprintf(&b, "  → misma IP destino → misma IP pública (%d/%d pares): la IP se elige por destino\n", pairs, pairs)
			} else {
				b.WriteString("  → la IP pública cambia incluso hacia la misma IP destino\n")
			}
		}
	}
	if d := deltasA(r.A); d != "" {
		fmt.Fprintf(&b, "  → saltos de puerto entre destinos: %s\n", d)
	}

	a := r.Alloc
	fmt.Fprintf(&b, "\nB. %d sockets nuevos → %s\n", len(r.B), firstTarget(r.B))
	for i, o := range r.B {
		if o.Err != nil {
			fmt.Fprintf(&b, "  %2d ✗ %v\n", i+1, o.Err)
		} else {
			fmt.Fprintf(&b, "  %2d :%-5d → %s\n", i+1, o.Local, o.Mapped)
		}
	}
	fmt.Fprintf(&b, "  respuestas: %d/%d\n", a.Answers, len(r.B))
	if a.Answers >= 4 {
		same := "✓ siempre la misma"
		if len(a.PublicIPs) > 1 {
			same = "✗ cambia (" + strings.Join(a.PublicIPs, ", ") + ")"
		}
		fmt.Fprintf(&b, "  IP pública: %s\n", same)
		fmt.Fprintf(&b, "  puerto local conservado: %d/%d\n", a.Preserved, a.Answers)
		fmt.Fprintf(&b, "  saltos: %v (mediana |salto| = %d)\n", a.Deltas, a.MedianAbs)
		fmt.Fprintf(&b, "  rango: %d–%d (%d puertos)\n", a.Min, a.Max, int(a.Max)-int(a.Min))
	}
	fmt.Fprintf(&b, "  → asignación: %s\n", a.Kind)

	b.WriteString("\nC. Qué significa para el camino iterativo\n")
	switch {
	case r.Mapping == MapIndependent:
		b.WriteString("  No hace falta: el puerto no cambia por destino, el punching normal basta.\n")
	case a.Answers < 4 || r.Mapping == MapUnknown:
		b.WriteString("  Faltan datos: repetir el experimento.\n")
	case len(a.PublicIPs) > 1:
		b.WriteString("  Difícil: sockets hacia un mismo destino salen por IPs públicas distintas; el otro lado no sabe a cuál disparar.\n")
	default:
		k := max(len(r.IPs), 1)
		fmt.Fprintf(&b, "  Rango efectivo ≈ %d puertos.\n", a.Block)
		if k > 1 {
			fmt.Fprintf(&b, "  El NAT usa %d IPs públicas según el destino: el otro lado no sabe por cuál saldremos\n"+
				"  hacia él y tiene que repartir los disparos entre las %d (cifras ya multiplicadas).\n", k, k)
		}
		for _, s := range []int{64, 256} {
			m := k * ProbesFor(0.99, s, a.Block)
			fmt.Fprintf(&b, "  con %d sockets acá, el otro lado necesita ~%d disparos para 99 %% (con 1024: %.0f %%)\n",
				s, m, 100*HitChance(s, 1024/k, a.Block))
		}
		if a.Kind == AllocSequential {
			b.WriteString("  Secuencial: mejor que el azar, predecir el próximo puerto y probar una ventana chica.\n")
		}
	}
	return b.String()
}

func writeObs(b *strings.Builder, o Obs) {
	if o.Err != nil {
		fmt.Fprintf(b, "  %s\n     ✗ %v\n", o.Target, o.Err)
		return
	}
	fmt.Fprintf(b, "  %s\n     :%d → %s\n", o.Target, o.Local, o.Mapped)
}

func firstTarget(obs []Obs) string {
	if len(obs) == 0 {
		return "-"
	}
	return obs[0].Target
}

func deltasA(obs []Obs) string {
	var parts []string
	var prev *Obs
	for i := range obs {
		o := &obs[i]
		if o.Err != nil {
			continue
		}
		if prev != nil {
			parts = append(parts, fmt.Sprintf("%+d", int(o.Mapped.Port())-int(prev.Mapped.Port())))
		}
		prev = o
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ")
}

// TextAnon es Text con nuestras IPs reemplazadas (LOCAL, PUB1, PUB2…): para
// pegar en el repo público sin publicar la IP de la casa o del celular. Los
// puertos y las IPs de los servidores STUN (públicos) se mantienen.
func (r *Result) TextAnon() string {
	txt := r.Text()
	var pairs []string
	if r.Local.IsValid() {
		pairs = append(pairs, r.Local.Addr().String(), "LOCAL")
	}
	seen := map[netip.Addr]bool{}
	for _, o := range append(slices.Clone(r.A), r.B...) {
		if o.Err != nil || seen[o.Mapped.Addr()] {
			continue
		}
		seen[o.Mapped.Addr()] = true
		pairs = append(pairs, o.Mapped.Addr().String(), fmt.Sprintf("PUB%d", len(seen)))
	}
	return strings.NewReplacer(pairs...).Replace(txt)
}
