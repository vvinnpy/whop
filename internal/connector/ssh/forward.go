package sshconn

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"

	"golang.org/x/crypto/ssh"

	coreconnector "whop/internal/connector"
	"whop/internal/sshconfig"
)

const socks5Version = 5

// Forward starts one local, remote, or dynamic TCP forwarding listener and
// keeps it active until ctx is canceled or the listener fails.
func (c *Connector) Forward(ctx context.Context, host sshconfig.Host, spec coreconnector.ForwardSpec, ready func(coreconnector.ForwardStatus) error) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	client, err := c.Dial(ctx, host)
	if err != nil {
		return err
	}
	defer client.Close()

	listenAddress := spec.BindAddress()
	if spec.Kind == coreconnector.ForwardRemote {
		listener, err := client.Listen("tcp", listenAddress)
		if err != nil {
			return fmt.Errorf("forward remote %s on %s: %w", listenAddress, host.Alias, err)
		}
		return c.serveForward(ctx, client, listener, spec, ready)
	}

	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		return fmt.Errorf("forward listen %s: %w", listenAddress, err)
	}
	return c.serveForward(ctx, client, listener, spec, ready)
}

func (c *Connector) serveForward(ctx context.Context, client *ssh.Client, listener net.Listener, spec coreconnector.ForwardSpec, ready func(coreconnector.ForwardStatus) error) error {
	defer listener.Close()
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	status := coreconnector.ForwardStatus{
		Kind:          spec.Kind,
		ListenAddress: actualListenAddress(listener.Addr(), spec.BindHost),
	}
	if spec.Kind != coreconnector.ForwardDynamic {
		status.DestinationAddress = spec.DestinationAddress()
	}
	if ready != nil {
		if err := ready(status); err != nil {
			return err
		}
	}

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = listener.Close()
		case <-stop:
		}
	}()

	for {
		incoming, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("forward accept on %s: %w", status.ListenAddress, err)
		}
		go c.handleForwardConn(connCtx, client, incoming, spec)
	}
}

func (c *Connector) handleForwardConn(ctx context.Context, client *ssh.Client, incoming net.Conn, spec coreconnector.ForwardSpec) {
	defer incoming.Close()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = incoming.Close()
		case <-stop:
		}
	}()

	destination := spec.DestinationAddress()
	var outgoing net.Conn
	var err error
	switch spec.Kind {
	case coreconnector.ForwardDynamic:
		var requested string
		requested, err = readSOCKS5Request(incoming)
		if err != nil {
			return
		}
		outgoing, err = client.DialContext(ctx, "tcp", requested)
		if err != nil {
			writeSOCKS5Reply(incoming, 5, nil)
			return
		}
		if err = writeSOCKS5Reply(incoming, 0, outgoing.LocalAddr()); err != nil {
			_ = outgoing.Close()
			return
		}
	case coreconnector.ForwardLocal:
		outgoing, err = client.DialContext(ctx, "tcp", destination)
		if err != nil {
			return
		}
	case coreconnector.ForwardRemote:
		var dialer net.Dialer
		outgoing, err = dialer.DialContext(ctx, "tcp", destination)
		if err != nil {
			return
		}
	default:
		return
	}
	defer outgoing.Close()
	bridgeConnections(ctx, incoming, outgoing)
}

func actualListenAddress(addr net.Addr, fallbackHost string) string {
	if addr == nil {
		return fallbackHost
	}
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = fallbackHost
	}
	return net.JoinHostPort(host, port)
}

func bridgeConnections(ctx context.Context, left, right net.Conn) {
	done := make(chan struct{}, 2)
	copyOne := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if closer, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = closer.CloseWrite()
		} else {
			_ = dst.Close()
		}
		done <- struct{}{}
	}
	go copyOne(left, right)
	go copyOne(right, left)

	finished := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = left.Close()
			_ = right.Close()
		case <-finished:
		}
	}()
	<-done
	<-done
	close(finished)
}

func readSOCKS5Request(conn net.Conn) (string, error) {
	var greeting [2]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil {
		return "", fmt.Errorf("socks5 greeting: %w", err)
	}
	if greeting[0] != socks5Version {
		return "", fmt.Errorf("socks5 unsupported version %d", greeting[0])
	}
	methods := make([]byte, int(greeting[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return "", fmt.Errorf("socks5 methods: %w", err)
	}
	noAuth := false
	for _, method := range methods {
		if method == 0 {
			noAuth = true
			break
		}
	}
	if !noAuth {
		_, _ = conn.Write([]byte{socks5Version, 0xff})
		return "", errors.New("socks5 client does not support no-auth")
	}
	if _, err := conn.Write([]byte{socks5Version, 0}); err != nil {
		return "", fmt.Errorf("socks5 select method: %w", err)
	}

	var request [4]byte
	if _, err := io.ReadFull(conn, request[:]); err != nil {
		return "", fmt.Errorf("socks5 request: %w", err)
	}
	if request[0] != socks5Version {
		return "", fmt.Errorf("socks5 unsupported request version %d", request[0])
	}
	if request[1] != 1 {
		_ = writeSOCKS5Reply(conn, 7, nil)
		return "", fmt.Errorf("socks5 unsupported command %d", request[1])
	}

	var host string
	switch request[3] {
	case 1:
		var address [4]byte
		if _, err := io.ReadFull(conn, address[:]); err != nil {
			return "", fmt.Errorf("socks5 read IPv4: %w", err)
		}
		host = net.IP(address[:]).String()
	case 3:
		var length [1]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			return "", fmt.Errorf("socks5 read domain length: %w", err)
		}
		name := make([]byte, int(length[0]))
		if _, err := io.ReadFull(conn, name); err != nil {
			return "", fmt.Errorf("socks5 read domain: %w", err)
		}
		host = string(name)
	case 4:
		var address [16]byte
		if _, err := io.ReadFull(conn, address[:]); err != nil {
			return "", fmt.Errorf("socks5 read IPv6: %w", err)
		}
		host = net.IP(address[:]).String()
	default:
		_ = writeSOCKS5Reply(conn, 8, nil)
		return "", fmt.Errorf("socks5 unsupported address type %d", request[3])
	}
	if host == "" {
		return "", errors.New("socks5 empty destination host")
	}
	var portBytes [2]byte
	if _, err := io.ReadFull(conn, portBytes[:]); err != nil {
		return "", fmt.Errorf("socks5 read port: %w", err)
	}
	port := binary.BigEndian.Uint16(portBytes[:])
	if port == 0 {
		return "", errors.New("socks5 destination port is zero")
	}
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}

func writeSOCKS5Reply(conn net.Conn, reply byte, bound net.Addr) error {
	host := net.IPv4zero
	port := 0
	if tcpAddr, ok := bound.(*net.TCPAddr); ok {
		host = tcpAddr.IP
		port = tcpAddr.Port
	}
	ip4 := host.To4()
	if ip4 == nil {
		ip4 = net.IPv4zero.To4()
	}
	response := []byte{socks5Version, reply, 0, 1, ip4[0], ip4[1], ip4[2], ip4[3], 0, 0}
	binary.BigEndian.PutUint16(response[8:], uint16(port))
	_, err := conn.Write(response)
	return err
}
