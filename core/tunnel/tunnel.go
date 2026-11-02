// Package tunnel monta un canal confiable (QUIC) sobre el socket del hole punching.
//
// El punching nos deja un "agujero" UDP entre el celular y el PC, pero UDP pierde
// y desordena paquetes. SSH necesita un flujo de bytes confiable, así que encima
// del mismo socket va QUIC: retransmisiones, control de congestión y varios
// streams independientes. Cada stream es una "tubería" (hoy: una sesión SSH).
//
//	celular                                        PC (agente)
//	 SSH cliente ── stream QUIC ──┐        ┌── stream QUIC ── SSH servidor ── pty ── tmux
//	                    QUIC Dial ┤ socket ├ QUIC Listen
//	                              └─ UDP ──┘   (el mismo del hole punching)
//
// QUIC exige TLS. El certificado del PC es autofirmado y el cliente no lo valida:
// la autenticación real (llave del celular, llave del PC) la hace SSH por dentro,
// de punta a punta. TLS aquí solo es el envoltorio que pide el protocolo.
package tunnel

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"math/big"
	"net"
	"os"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/jmbustos1/remote-dev-access/core/logx"
	"github.com/jmbustos1/remote-dev-access/core/punch"
)

const alpn = "rda/1"

func init() {
	// quic-go avisa que no puede agrandar el buffer del socket porque nuestro
	// PacketConn no es un *net.UDPConn. Es esperado: el socket es de la sesión.
	os.Setenv("QUIC_GO_DISABLE_RECEIVE_BUFFER_WARNING", "true")
}

// Tiempos de QUIC. MaxIdleTimeout es generoso: si el celular se queda sin red un
// rato (metro, ascensor), la conexión aguanta y la terminal sigue donde estaba.
var (
	KeepAlivePeriod = 10 * time.Second
	MaxIdleTimeout  = 3 * time.Minute
)

func quicConfig() *quic.Config {
	return &quic.Config{
		KeepAlivePeriod:      KeepAlivePeriod,
		MaxIdleTimeout:       MaxIdleTimeout,
		HandshakeIdleTimeout: 15 * time.Second,
	}
}

// Stream es un stream QUIC visto como net.Conn (lo que espera x/crypto/ssh).
type Stream struct {
	*quic.Stream
	conn *quic.Conn
}

func (s Stream) LocalAddr() net.Addr  { return s.conn.LocalAddr() }
func (s Stream) RemoteAddr() net.Addr { return s.conn.RemoteAddr() }

// Close cierra ambos sentidos: Stream.Close de quic-go solo cierra la escritura.
func (s Stream) Close() error {
	s.CancelRead(0)
	return s.Stream.Close()
}

// Serve atiende conexiones QUIC sobre la sesión (lado PC) y entrega cada stream
// a handle en su propia goroutine. Termina cuando se cierra la sesión o ctx.
func Serve(ctx context.Context, s *punch.Session, handle func(Stream)) error {
	log := logx.For(logx.StageTunnel)
	cert, err := selfSigned()
	if err != nil {
		return err
	}
	tr := &quic.Transport{Conn: s.PacketConn()}
	ln, err := tr.Listen(&tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{alpn}}, quicConfig())
	if err != nil {
		return err
	}
	defer tr.Close()
	defer ln.Close()
	log.Info("canal QUIC esperando al otro lado")
	for {
		c, err := ln.Accept(ctx)
		if err != nil {
			return err
		}
		log.Info("canal QUIC establecido", "rtt", c.ConnectionStats().SmoothedRTT)
		go func() {
			for {
				st, err := c.AcceptStream(ctx)
				if err != nil {
					log.Info("canal QUIC cerrado", "motivo", err)
					return
				}
				go handle(Stream{st, c})
			}
		}()
	}
}

// Conn es el lado cliente del canal (celular / CLI).
type Conn struct {
	c  *quic.Conn
	tr *quic.Transport
}

// Dial abre el canal QUIC hacia el PC sobre una sesión ya conectada.
func Dial(ctx context.Context, s *punch.Session) (*Conn, error) {
	log := logx.For(logx.StageTunnel)
	tr := &quic.Transport{Conn: s.PacketConn()}
	c, err := tr.Dial(ctx, punch.PeerAddr, &tls.Config{
		InsecureSkipVerify: true, // ver el comentario del paquete: autentica SSH
		NextProtos:         []string{alpn},
	}, quicConfig())
	if err != nil {
		return nil, err
	}
	log.Info("canal QUIC establecido", "rtt", c.ConnectionStats().SmoothedRTT)
	return &Conn{c: c, tr: tr}, nil
}

// OpenStream abre una tubería nueva hacia el PC.
func (c *Conn) OpenStream(ctx context.Context) (Stream, error) {
	st, err := c.c.OpenStreamSync(ctx)
	if err != nil {
		return Stream{}, err
	}
	return Stream{st, c.c}, nil
}

// Done se cierra cuando la conexión QUIC muere (p. ej. MaxIdleTimeout sin red).
func (c *Conn) Done() <-chan struct{} { return c.c.Context().Done() }

func (c *Conn) Close() error {
	err := c.c.CloseWithError(0, "cerrado")
	c.tr.Close()
	return err
}

// selfSigned genera un certificado efímero (vive lo que dura la sesión).
func selfSigned() (tls.Certificate, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}, nil
}

// IsClosed indica si err significa "se cerró la sesión" (no es una falla).
func IsClosed(err error) bool {
	return errors.Is(err, net.ErrClosed) || errors.Is(err, context.Canceled)
}
