// Package netx agrupa utilidades de red compartidas.
package netx

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"time"
)

// resolver usa el del sistema, salvo que no exista /etc/resolv.conf (Android /
// Termux con binario Go puro): ahí el resolver de Go no sabe a quién preguntar,
// así que forzamos un DNS público.
var resolver = func() *net.Resolver {
	if _, err := os.Stat("/etc/resolv.conf"); err == nil {
		return net.DefaultResolver
	}
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 3 * time.Second}
			return d.DialContext(ctx, network, "1.1.1.1:53")
		},
	}
}()

// ResolveUDP4 resuelve "host:puerto" a una dirección UDP IPv4.
func ResolveUDP4(ctx context.Context, hostport string) (*net.UDPAddr, error) {
	return ResolveUDP(ctx, hostport, "ip4")
}

// ResolveUDP resuelve "host:puerto" para la familia dada ("ip4" o "ip6").
func ResolveUDP(ctx context.Context, hostport, family string) (*net.UDPAddr, error) {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		return nil, err
	}
	p, err := net.DefaultResolver.LookupPort(ctx, "udp", port)
	if err != nil {
		return nil, err
	}
	ips, err := resolver.LookupIP(ctx, family, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("%s no tiene direcciones %s", host, family)
	}
	return &net.UDPAddr{IP: ips[0], Port: p}, nil
}

// LocalAddr devuelve la IP local real (la de la interfaz con salida a internet,
// no 0.0.0.0) junto al puerto dado. Sirve para detectar si estamos sin NAT y
// para ofrecer la dirección de red local cuando ambos lados están en el mismo wifi.
func LocalAddr(port uint16) netip.AddrPort {
	c, err := net.Dial("udp4", "8.8.8.8:53") // no envía nada, solo elige la ruta
	if err != nil {
		return netip.AddrPortFrom(netip.IPv4Unspecified(), port)
	}
	defer c.Close()
	ip, _ := netip.AddrFromSlice(c.LocalAddr().(*net.UDPAddr).IP.To4())
	return netip.AddrPortFrom(ip, port)
}
