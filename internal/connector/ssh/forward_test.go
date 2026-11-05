package sshconn

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	coreconnector "whop/internal/connector"
	"whop/internal/sshconfig"
)

func startEchoServer(t *testing.T) (string, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	addr := listener.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

func runForwardStatus(t *testing.T, ctx context.Context, connector *Connector, host sshconfig.Host, spec coreconnector.ForwardSpec) (coreconnector.ForwardStatus, <-chan error) {
	t.Helper()
	ready := make(chan coreconnector.ForwardStatus, 1)
	errs := make(chan error, 1)
	go func() {
		errs <- connector.Forward(ctx, host, spec, func(status coreconnector.ForwardStatus) error {
			ready <- status
			return nil
		})
	}()
	select {
	case status := <-ready:
		return status, errs
	case err := <-errs:
		t.Fatalf("forward stopped before ready: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("forward did not become ready")
	}
	return coreconnector.ForwardStatus{}, errs
}

func assertEcho(t *testing.T, address, payload string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(conn, payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != payload {
		t.Fatalf("echo = %q, want %q", got, payload)
	}
}

func TestLocalForward(t *testing.T) {
	echoHost, echoPort := startEchoServer(t)
	keyPath, public := writeClientKey(t)
	server := newTestSSHServer(t, public)
	host := sshconfig.Host{
		Alias:         "forward-target",
		HostName:      "127.0.0.1",
		Port:          server.port(),
		User:          "tester",
		IdentityFiles: []string{keyPath},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	status, errs := runForwardStatus(t, ctx, newTestConnector(), host, coreconnector.ForwardSpec{
		Kind:            coreconnector.ForwardLocal,
		BindHost:        "127.0.0.1",
		BindPort:        0,
		DestinationHost: echoHost,
		DestinationPort: echoPort,
	})
	assertEcho(t, status.ListenAddress, "local-forward")
	cancel()
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
}

func TestRemoteForward(t *testing.T) {
	echoHost, echoPort := startEchoServer(t)
	keyPath, public := writeClientKey(t)
	server := newTestSSHServer(t, public)
	host := sshconfig.Host{
		Alias:         "forward-target",
		HostName:      "127.0.0.1",
		Port:          server.port(),
		User:          "tester",
		IdentityFiles: []string{keyPath},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	status, errs := runForwardStatus(t, ctx, newTestConnector(), host, coreconnector.ForwardSpec{
		Kind:            coreconnector.ForwardRemote,
		BindHost:        "127.0.0.1",
		BindPort:        0,
		DestinationHost: echoHost,
		DestinationPort: echoPort,
	})
	assertEcho(t, status.ListenAddress, "remote-forward")
	cancel()
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
}

func TestDynamicForwardSOCKS5(t *testing.T) {
	echoHost, echoPort := startEchoServer(t)
	keyPath, public := writeClientKey(t)
	server := newTestSSHServer(t, public)
	host := sshconfig.Host{
		Alias:         "forward-target",
		HostName:      "127.0.0.1",
		Port:          server.port(),
		User:          "tester",
		IdentityFiles: []string{keyPath},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	status, errs := runForwardStatus(t, ctx, newTestConnector(), host, coreconnector.ForwardSpec{
		Kind:     coreconnector.ForwardDynamic,
		BindHost: "127.0.0.1",
		BindPort: 0,
	})

	conn, err := net.DialTimeout("tcp", status.ListenAddress, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	var method [2]byte
	if _, err := io.ReadFull(conn, method[:]); err != nil {
		t.Fatal(err)
	}
	if method != [2]byte{5, 0} {
		t.Fatalf("SOCKS method reply = %v", method)
	}
	name := []byte(echoHost)
	request := []byte{5, 1, 0, 3, byte(len(name))}
	request = append(request, name...)
	var port [2]byte
	binary.BigEndian.PutUint16(port[:], uint16(echoPort))
	request = append(request, port[:]...)
	if _, err := conn.Write(request); err != nil {
		t.Fatal(err)
	}
	var reply [10]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		t.Fatal(err)
	}
	if reply[0] != 5 || reply[1] != 0 {
		t.Fatalf("SOCKS connect reply = %v", reply)
	}
	if _, err := io.WriteString(conn, "dynamic-forward"); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("dynamic-forward"))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "dynamic-forward" {
		t.Fatalf("echo = %q", got)
	}
	cancel()
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
}

func TestReadSOCKS5RequestRejectsUnsupportedCommand(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		server, err := listener.Accept()
		if err != nil {
			return
		}
		defer server.Close()
		_, _ = server.Write([]byte{5, 1, 0})
		var selection [2]byte
		_, _ = io.ReadFull(server, selection[:])
		_, _ = server.Write([]byte{5, 7, 0, 1})
		var reply [10]byte
		_, _ = io.ReadFull(server, reply[:])
	}()
	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := readSOCKS5Request(client); err == nil {
		t.Fatal("unsupported SOCKS command was accepted")
	}
	<-done
}

func TestForwardSpecAddresses(t *testing.T) {
	spec := coreconnector.ForwardSpec{Kind: coreconnector.ForwardLocal, BindHost: "::1", BindPort: 8080, DestinationHost: "db", DestinationPort: 5432}
	if got := spec.BindAddress(); got != net.JoinHostPort("::1", strconv.Itoa(8080)) {
		t.Fatalf("bind = %q", got)
	}
	if got := spec.DestinationAddress(); got != net.JoinHostPort("db", strconv.Itoa(5432)) {
		t.Fatalf("destination = %q", got)
	}
}
