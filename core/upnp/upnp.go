// Package upnp le pide al router de la casa (UPnP IGD) que reenvíe un puerto UDP
// externo hacia nuestro socket. Con eso el router deja entrar paquetes desde
// CUALQUIER ip:puerto, que es justo lo que hace falta cuando el otro lado está
// detrás de un CGNAT que cambia de puerto según el destino.
package upnp

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/huin/goupnp/dcps/internetgateway2"

	"github.com/jmbustos1/remote-dev-access/core/logx"
	"github.com/jmbustos1/remote-dev-access/core/netx"
)

// igd es lo común a WANIPConnection1/2 y WANPPPConnection1.
type igd interface {
	AddPortMappingCtx(ctx context.Context, remoteHost string, extPort uint16, proto string,
		intPort uint16, intClient string, enabled bool, desc string, lease uint32) error
	DeletePortMappingCtx(ctx context.Context, remoteHost string, extPort uint16, proto string) error
	GetExternalIPAddressCtx(ctx context.Context) (string, error)
}

// Mapping es un puerto abierto en el router. Cerrarlo lo elimina.
type Mapping struct {
	External netip.AddrPort
	client   igd
}

// MapUDP busca el router y reenvía un puerto UDP externo a internalPort de este
// equipo. Intenta usar el mismo número de puerto; si está ocupado, prueba otros.
func MapUDP(ctx context.Context, internalPort uint16, desc string, lease time.Duration) (*Mapping, error) {
	log := logx.For(logx.StagePunch)
	client, err := discover(ctx)
	if err != nil {
		return nil, err
	}
	extIPStr, err := client.GetExternalIPAddressCtx(ctx)
	if err != nil {
		return nil, fmt.Errorf("el router no entregó su IP externa: %w", err)
	}
	extIP, err := netip.ParseAddr(extIPStr)
	if err != nil {
		return nil, fmt.Errorf("IP externa inválida %q: %w", extIPStr, err)
	}
	internal := netx.LocalAddr(internalPort).Addr().String()

	ports := []uint16{internalPort}
	for i := 0; i < 3; i++ {
		ports = append(ports, uint16(20000+rand.IntN(40000)))
	}
	var lastErr error
	for _, ext := range ports {
		secs := uint32(lease / time.Second)
		err := client.AddPortMappingCtx(ctx, "", ext, "UDP", internalPort, internal, true, desc, secs)
		if err != nil && secs != 0 && strings.Contains(err.Error(), "725") {
			// OnlyPermanentLeasesSupported: el router no acepta caducidad.
			err = client.AddPortMappingCtx(ctx, "", ext, "UDP", internalPort, internal, true, desc, 0)
		}
		if err != nil {
			lastErr = err
			log.Debug("UPnP rechazó el puerto", "external", ext, "err", err)
			continue
		}
		m := &Mapping{External: netip.AddrPortFrom(extIP, ext), client: client}
		log.Info("UPnP: puerto abierto en el router", "external", m.External, "internal", fmt.Sprintf("%s:%d", internal, internalPort))
		return m, nil
	}
	return nil, fmt.Errorf("el router rechazó el reenvío: %w", lastErr)
}

// Close elimina el reenvío del router.
func (m *Mapping) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := m.client.DeletePortMappingCtx(ctx, "", m.External.Port(), "UDP")
	if err == nil {
		logx.For(logx.StagePunch).Info("UPnP: puerto cerrado en el router", "external", m.External)
	}
	return err
}

// discover busca los tres tipos de servicio IGD en paralelo (cada búsqueda SSDP
// espera unos segundos) y prefiere WANIPConnection2 > 1 > PPP.
func discover(ctx context.Context) (igd, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var found [3]igd
	var wg sync.WaitGroup
	search := func(i int, f func(context.Context) (igd, error)) {
		defer wg.Done()
		if c, err := f(ctx); err == nil {
			found[i] = c
		}
	}
	wg.Add(3)
	go search(0, func(ctx context.Context) (igd, error) {
		cs, _, err := internetgateway2.NewWANIPConnection2ClientsCtx(ctx)
		if err != nil || len(cs) == 0 {
			return nil, errors.New("no")
		}
		return cs[0], nil
	})
	go search(1, func(ctx context.Context) (igd, error) {
		cs, _, err := internetgateway2.NewWANIPConnection1ClientsCtx(ctx)
		if err != nil || len(cs) == 0 {
			return nil, errors.New("no")
		}
		return cs[0], nil
	})
	go search(2, func(ctx context.Context) (igd, error) {
		cs, _, err := internetgateway2.NewWANPPPConnection1ClientsCtx(ctx)
		if err != nil || len(cs) == 0 {
			return nil, errors.New("no")
		}
		return cs[0], nil
	})
	wg.Wait()
	for _, c := range found {
		if c != nil {
			return c, nil
		}
	}
	return nil, errors.New("no se encontró un router con UPnP")
}
