package localipc

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOwnerLocalTransport(t *testing.T) {
	root, err := os.MkdirTemp("", "cf-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	endpoint := Endpoint(filepath.Join(root, "runtime"))
	listener, err := Listen(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
		_ = Remove(endpoint)
	})
	serverError := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			serverError <- err
			return
		}
		defer connection.Close()
		line, err := bufio.NewReader(connection).ReadString('\n')
		if err == nil && line != "ping\n" {
			err = fmt.Errorf("request = %q", line)
		}
		if err == nil {
			_, err = io.WriteString(connection, "pong\n")
		}
		serverError <- err
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	connection, err := DialContext(ctx, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := io.WriteString(connection, "ping\n"); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(connection).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if line != "pong\n" {
		t.Fatalf("response = %q", line)
	}
	if err := <-serverError; err != nil {
		t.Fatal(err)
	}
	if second, err := Listen(endpoint); err == nil {
		_ = second.Close()
		t.Fatal("second Listen() succeeded on an occupied endpoint")
	}
}
