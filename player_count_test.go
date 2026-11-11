package main

import (
	"bytes"
	"errors"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	enc "github.com/Raqbit/mc-pinger/encoding"
	"go.uber.org/zap"
)

func TestShouldAnnounceStop(t *testing.T) {
	tests := []struct {
		name          string
		announceDelay time.Duration
		skipWhenEmpty bool
		online        int
		queryErr      error
		wantQueried   bool
		want          bool
	}{
		{name: "no delay configured", announceDelay: 0, skipWhenEmpty: false, want: false},
		{name: "no delay configured with skip enabled", announceDelay: 0, skipWhenEmpty: true, want: false},
		{name: "skip disabled", announceDelay: time.Minute, skipWhenEmpty: false, want: true},
		{name: "empty server", announceDelay: time.Minute, skipWhenEmpty: true, online: 0, wantQueried: true, want: false},
		{name: "players online", announceDelay: time.Minute, skipWhenEmpty: true, online: 2, wantQueried: true, want: true},
		{name: "query failed", announceDelay: time.Minute, skipWhenEmpty: true, queryErr: errors.New("connection refused"), wantQueried: true, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queried := false
			onlinePlayers := func() (int, error) {
				queried = true
				return tt.online, tt.queryErr
			}

			got := shouldAnnounceStop(zap.NewNop(), tt.announceDelay, tt.skipWhenEmpty, onlinePlayers)

			if got != tt.want {
				t.Errorf("shouldAnnounceStop() = %v, want %v", got, tt.want)
			}
			if queried != tt.wantQueried {
				t.Errorf("player count queried = %v, want %v", queried, tt.wantQueried)
			}
		})
	}
}

func TestQueryOnlinePlayers(t *testing.T) {
	tests := []struct {
		name    string
		status  string
		want    int
		wantErr bool
	}{
		{
			name:   "empty server",
			status: `{"version":{"name":"1.21.4","protocol":769},"players":{"max":20,"online":0},"description":"A Minecraft Server"}`,
			want:   0,
		},
		{
			name:   "players online",
			status: `{"version":{"name":"1.21.4","protocol":769},"players":{"max":20,"online":3,"sample":[]},"description":{"text":"A Minecraft Server"}}`,
			want:   3,
		},
		{
			name:    "missing player info",
			status:  `{}`,
			wantErr: true,
		},
		{
			name:    "malformed status",
			status:  `not json`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, port := startStatusServer(t, func(conn net.Conn) {
				if err := serveStatus(conn, tt.status); err != nil {
					t.Errorf("failed to serve status: %v", err)
				}
			})

			got, err := queryOnlinePlayers(host, port, 5*time.Second)

			if (err != nil) != tt.wantErr {
				t.Fatalf("queryOnlinePlayers() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("queryOnlinePlayers() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestQueryOnlinePlayers_Unresponsive(t *testing.T) {
	release := make(chan struct{})
	// deferred instead of registered as a cleanup, since the handler has to return before the server can shut down
	defer close(release)
	host, port := startStatusServer(t, func(net.Conn) {
		// accept the connection, but never respond, like a paused server would
		<-release
	})

	start := time.Now()
	_, err := queryOnlinePlayers(host, port, 100*time.Millisecond)

	if err == nil {
		t.Fatal("expected an error from an unresponsive server")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("query took %s, which is well beyond the timeout", elapsed)
	}
}

func TestQueryOnlinePlayers_NotListening(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, port := splitHostPort(t, listener.Addr())
	// free up the port so that nothing is listening on it
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := queryOnlinePlayers(host, port, time.Second); err == nil {
		t.Fatal("expected an error when nothing is listening")
	}
}

func TestQueryOnlinePlayers_InvalidPort(t *testing.T) {
	for _, port := range []int{-1, 0, 65536} {
		if _, err := queryOnlinePlayers("localhost", port, time.Second); err == nil {
			t.Errorf("expected an error for port %d", port)
		}
	}
}

// startStatusServer listens on an ephemeral local port and passes each accepted connection to handler
func startStatusServer(t *testing.T, handler func(conn net.Conn)) (string, int) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			handler(conn)
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
	})

	return splitHostPort(t, listener.Addr())
}

func splitHostPort(t *testing.T, addr net.Addr) (string, int) {
	t.Helper()

	host, portStr, err := net.SplitHostPort(addr.String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}

// serveStatus acts as the server side of a server list ping by consuming the handshake and
// status request packets and then responding with the given status JSON
func serveStatus(conn net.Conn, status string) error {
	for i := 0; i < 2; i++ {
		length, err := enc.ReadVarInt(conn)
		if err != nil {
			return err
		}
		if _, err := io.CopyN(io.Discard, conn, int64(length)); err != nil {
			return err
		}
	}

	var payload bytes.Buffer
	// packet ID of the status response
	if err := enc.WriteVarInt(&payload, 0x00); err != nil {
		return err
	}
	if err := enc.WriteString(&payload, enc.String(status)); err != nil {
		return err
	}

	if err := enc.WriteVarInt(conn, enc.VarInt(payload.Len())); err != nil {
		return err
	}
	_, err := conn.Write(payload.Bytes())
	return err
}
