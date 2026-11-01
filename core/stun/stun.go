// Package stun implementa el mínimo de STUN (RFC 5389) para responder una sola
// pregunta: "¿cómo me ve internet?", es decir, qué IP:puerto público le asignó
// el NAT a nuestro socket UDP.
//
// Anatomía de un mensaje STUN (todo big-endian):
//
//	 0                   1                   2                   3
//	 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|0 0|     Tipo de mensaje       |     Largo de los atributos    |
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|                Magic cookie = 0x2112A442                      |
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|              Transaction ID (12 bytes aleatorios)             |
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|              Atributos (TLV: tipo, largo, valor)...           |
//
// La pregunta es un "Binding Request" de 20 bytes sin atributos. La respuesta
// trae el atributo XOR-MAPPED-ADDRESS con nuestra dirección pública.
package stun

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"
)

const (
	headerSize  = 20
	magicCookie = 0x2112A442

	typeBindingRequest  = 0x0001
	typeBindingResponse = 0x0101

	attrMappedAddress    = 0x0001 // versión antigua, dirección en claro
	attrXORMappedAddress = 0x0020 // dirección "XOReada" con el cookie

	familyIPv4 = 0x01
	familyIPv6 = 0x02
)

// TxID identifica una pregunta para emparejarla con su respuesta.
type TxID [12]byte

// NewBindingRequest arma la pregunta de 20 bytes.
func NewBindingRequest() (TxID, []byte, error) {
	var id TxID
	if _, err := rand.Read(id[:]); err != nil {
		return id, nil, err
	}
	msg := make([]byte, headerSize)
	binary.BigEndian.PutUint16(msg[0:2], typeBindingRequest)
	binary.BigEndian.PutUint16(msg[2:4], 0) // sin atributos
	binary.BigEndian.PutUint32(msg[4:8], magicCookie)
	copy(msg[8:20], id[:])
	return id, msg, nil
}

// ParseBindingResponse valida la respuesta y extrae la dirección pública.
func ParseBindingResponse(msg []byte, want TxID) (netip.AddrPort, error) {
	if len(msg) < headerSize {
		return netip.AddrPort{}, errors.New("respuesta demasiado corta")
	}
	if t := binary.BigEndian.Uint16(msg[0:2]); t != typeBindingResponse {
		return netip.AddrPort{}, fmt.Errorf("tipo de mensaje inesperado 0x%04x", t)
	}
	if binary.BigEndian.Uint32(msg[4:8]) != magicCookie {
		return netip.AddrPort{}, errors.New("magic cookie inválido: no es STUN")
	}
	var got TxID
	copy(got[:], msg[8:20])
	if got != want {
		return netip.AddrPort{}, errors.New("transaction ID no coincide")
	}
	length := int(binary.BigEndian.Uint16(msg[2:4]))
	if headerSize+length > len(msg) {
		return netip.AddrPort{}, errors.New("largo de atributos inválido")
	}

	// Recorremos los atributos TLV. Cada valor se rellena a múltiplos de 4.
	var fallback netip.AddrPort
	attrs := msg[headerSize : headerSize+length]
	for len(attrs) >= 4 {
		typ := binary.BigEndian.Uint16(attrs[0:2])
		l := int(binary.BigEndian.Uint16(attrs[2:4]))
		if 4+l > len(attrs) {
			return netip.AddrPort{}, errors.New("atributo truncado")
		}
		val := attrs[4 : 4+l]
		switch typ {
		case attrXORMappedAddress:
			return decodeAddress(val, true, got)
		case attrMappedAddress:
			if ap, err := decodeAddress(val, false, got); err == nil {
				fallback = ap
			}
		}
		padded := (l + 3) &^ 3
		if 4+padded > len(attrs) {
			break
		}
		attrs = attrs[4+padded:]
	}
	if fallback.IsValid() {
		return fallback, nil
	}
	return netip.AddrPort{}, errors.New("la respuesta no trae dirección mapeada")
}

// decodeAddress lee [0x00][familia][puerto 2B][IP 4 o 16B].
// Con xor=true el puerto viene XOR con los 16 bits altos del cookie y la IP
// XOR con el cookie (+ transaction ID en IPv6). Se hace para que routers
// "inteligentes" no reescriban la IP que viaja dentro del paquete.
func decodeAddress(val []byte, xor bool, id TxID) (netip.AddrPort, error) {
	if len(val) < 4 {
		return netip.AddrPort{}, errors.New("atributo de dirección corto")
	}
	family := val[1]
	port := binary.BigEndian.Uint16(val[2:4])

	var key [16]byte
	binary.BigEndian.PutUint32(key[0:4], magicCookie)
	copy(key[4:], id[:])

	if xor {
		port ^= uint16(magicCookie >> 16)
	}
	switch family {
	case familyIPv4:
		if len(val) < 8 {
			return netip.AddrPort{}, errors.New("IPv4 truncada")
		}
		var ip [4]byte
		copy(ip[:], val[4:8])
		if xor {
			for i := range ip {
				ip[i] ^= key[i]
			}
		}
		return netip.AddrPortFrom(netip.AddrFrom4(ip), port), nil
	case familyIPv6:
		if len(val) < 20 {
			return netip.AddrPort{}, errors.New("IPv6 truncada")
		}
		var ip [16]byte
		copy(ip[:], val[4:20])
		if xor {
			for i := range ip {
				ip[i] ^= key[i]
			}
		}
		return netip.AddrPortFrom(netip.AddrFrom16(ip), port), nil
	}
	return netip.AddrPort{}, fmt.Errorf("familia de dirección desconocida 0x%02x", family)
}

// Query manda un Binding Request al servidor desde conn y espera la respuesta.
// Reintenta porque UDP puede perder paquetes. Usar siempre el mismo conn es
// clave: el NAT asigna el mapeo por socket, y ese socket es el que después
// usaremos para el hole punching.
func Query(conn *net.UDPConn, server *net.UDPAddr, timeout time.Duration, attempts int) (netip.AddrPort, time.Duration, error) {
	id, req, err := NewBindingRequest()
	if err != nil {
		return netip.AddrPort{}, 0, err
	}
	buf := make([]byte, 1500)
	var lastErr error
	for i := 0; i < attempts; i++ {
		start := time.Now()
		if _, err := conn.WriteToUDP(req, server); err != nil {
			return netip.AddrPort{}, 0, err
		}
		deadline := start.Add(timeout)
		for {
			if err := conn.SetReadDeadline(deadline); err != nil {
				return netip.AddrPort{}, 0, err
			}
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				lastErr = err
				break // timeout → reintento
			}
			if !from.IP.Equal(server.IP) || from.Port != server.Port {
				continue // paquete de otro origen, lo ignoramos
			}
			ap, err := ParseBindingResponse(buf[:n], id)
			if err != nil {
				lastErr = err
				continue
			}
			return ap, time.Since(start), nil
		}
	}
	return netip.AddrPort{}, 0, fmt.Errorf("sin respuesta tras %d intentos: %w", attempts, lastErr)
}

// EncodeBindingResponse arma una respuesta de Binding con XOR-MAPPED-ADDRESS
// (la usan los tests como servidor STUN falso).
func EncodeBindingResponse(id TxID, addr netip.AddrPort) []byte {
	ip := addr.Addr().Unmap()
	family, ipLen := byte(familyIPv4), 4
	if ip.Is6() {
		family, ipLen = familyIPv6, 16
	}
	var key [16]byte
	binary.BigEndian.PutUint32(key[0:4], magicCookie)
	copy(key[4:], id[:])

	val := make([]byte, 4+ipLen)
	val[1] = family
	binary.BigEndian.PutUint16(val[2:4], addr.Port()^uint16(magicCookie>>16))
	raw := ip.AsSlice()
	for i := range raw {
		val[4+i] = raw[i] ^ key[i]
	}

	msg := make([]byte, headerSize+4+len(val))
	binary.BigEndian.PutUint16(msg[0:2], typeBindingResponse)
	binary.BigEndian.PutUint16(msg[2:4], uint16(4+len(val)))
	binary.BigEndian.PutUint32(msg[4:8], magicCookie)
	copy(msg[8:20], id[:])
	binary.BigEndian.PutUint16(msg[20:22], attrXORMappedAddress)
	binary.BigEndian.PutUint16(msg[22:24], uint16(len(val)))
	copy(msg[24:], val)
	return msg
}
