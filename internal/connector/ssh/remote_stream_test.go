package sshconn

import (
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

func TestRemoteStreamReadDeadlineCanBeCleared(t *testing.T) {
	reader, writer := io.Pipe()
	stream := newRemoteStream(reader, io.Discard, reader.Close)
	defer stream.Close()

	if err := stream.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	_, err := stream.Read(make([]byte, 16))
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("read error = %v, want deadline exceeded", err)
	}

	if _, err := writer.Write([]byte("after timeout")); err != nil {
		t.Fatal(err)
	}
	if err := stream.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 32)
	deadline := time.Now().Add(time.Second)
	for {
		n, err := stream.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			if string(buf[:n]) != "after timeout" {
				t.Fatalf("read = %q", buf[:n])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timeout waiting for buffered data")
		}
	}
}

func TestRemoteStreamCloseUnblocksRead(t *testing.T) {
	reader, _ := io.Pipe()
	stream := newRemoteStream(reader, io.Discard, reader.Close)
	done := make(chan error, 1)
	go func() {
		_, err := stream.Read(make([]byte, 16))
		done <- err
	}()
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected read error after close")
		}
	case <-time.After(time.Second):
		t.Fatal("read remained blocked after close")
	}
}

func TestRemoteStreamWrite(t *testing.T) {
	reader, writer := io.Pipe()
	stream := newRemoteStream(reader, writer, reader.Close)
	defer stream.Close()
	go func() { _, _ = writer.Write([]byte("ok")) }()
	buf := make([]byte, 2)
	if _, err := io.ReadFull(stream, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "ok" {
		t.Fatalf("read = %q", buf)
	}
}
