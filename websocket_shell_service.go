package main

import (
	// "bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
	"go.uber.org/zap"
	// "golang.org/x/time/rate"
)

type MessageType string

const (
	// Client -> Server
	MessageTypeStdin MessageType = "stdin"
	// Server -> Client
	MessageTypeStdout  MessageType = "stdout"
	MessageTypeStderr  MessageType = "stderr"
	MessageTypeWelcome MessageType = "welcome"
	MessageTypeAuthErr MessageType = "auth_err"
)

type Message struct {
	Type        MessageType `json:"type"`
	Content     string      `json:"content,omitempty"`
	RecentLines []string    `json:"recent_lines,omitempty"`
	LineCount   int         `json:"line_count,omitempty"`
	Time        time.Time   `json:"time"`
}

type WsClient struct {
	c          *websocket.Conn
	w          http.ResponseWriter
	r          http.Request
	writeMutex sync.Mutex
}

type websocketServer struct {
	logger  *zap.Logger
	clients map[uuid.UUID]*WsClient
	mu      sync.Mutex
}

func (s *websocketServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		s.logger.Error("websocket accept failed", zap.Error(err))
		return
	}
	defer c.CloseNow()

	s.mu.Lock()
	sessionId := uuid.New()
	s.clients[sessionId] = &WsClient{
		c,
		w,
		*r,
		sync.Mutex{},
	}
	s.mu.Unlock()

	s.logger.Info(fmt.Sprintf("Websocket connection opened with %v", r.RemoteAddr))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go heartbeatRoutine(ctx, s.logger, c, 30*time.Second)

	// l := rate.NewLimiter(rate.Every(time.Millisecond*100), 10)
	for {
		err = echo(c)
		if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
			s.logger.Info(fmt.Sprintf("Websocket connection closed with %v", r.RemoteAddr))
			return
		}
		if err != nil {
			s.logger.Error(fmt.Sprintf("failed to echo with %v", r.RemoteAddr), zap.Error(err))
			return
		}
	}
}

// heartbeatRoutine sends periodic pings and closes the connection if it fails.
func heartbeatRoutine(ctx context.Context, logger *zap.Logger, c *websocket.Conn, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

			start := time.Now()
			if err := c.Ping(pingCtx); err != nil {
				logger.Warn("ping failed", zap.Error(err))
				cancel()
				c.CloseNow()
				return
			}
			rtt := time.Since(start)
			cancel()

			logger.Debug("pong received", zap.Duration("rtt", rtt))
		}
	}
}

func echo(c *websocket.Conn /*stdin io.Writer*/) error {
	ctx := context.Background()

	for {
		typ, r, err := c.Reader(ctx)
		if err != nil {
			return err
		}

		if typ == websocket.MessageText {
			data, err := io.ReadAll(r)
			if err != nil {
				return err
			}
			// stdin.Write(data) // Send to Minecraft server stdin
			fmt.Println(string(data)) // Print line DEBUG
		}
	}
}

type wsWriter struct {
	server *websocketServer
	prefix string
}

func (b *wsWriter) Write(p []byte) (int, error) {
	msg := string(p)
	if b.prefix != "" {
		msg = b.prefix + msg
	}
	if b.server != nil {
		b.server.broadcast(msg)
	}
	fmt.Println("[wsWriter] " + msg)
	return len(p), nil
}

func (s *websocketServer) broadcast(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for id, client := range s.clients {
		client.writeMutex.Lock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		message := &Message{
			Type:    MessageTypeStdout,
			Content: string([]byte(msg)),
			Time:    time.Now(),
		}
		// err := client.c.Write(ctx, websocket.MessageText, )
		err := wsjson.Write(ctx, client.c, message)
		cancel()
		client.writeMutex.Unlock()

		if err != nil {
			s.logger.Error("failed to send message to client",
				zap.String("client", id.String()),
				zap.Error(err),
			)
			client.c.Close(websocket.StatusInternalError, "closing client")
			delete(s.clients, id)
		}
	}
}

func runWebsocketServer(logger *zap.Logger /*console *WsConsole*/, writer *wsWriter) error {
	l, err := net.Listen("tcp", "0.0.0.0:80")
	if err != nil {
		return err
	}
	logger.Info(fmt.Sprintf("Starting websocket on ws://%v", l.Addr()))

	s := &http.Server{
		Handler: &websocketServer{
			logger,
			//console,
			map[uuid.UUID]*WsClient{},
			sync.Mutex{},
		},
		ReadTimeout:  time.Second * 10,
		WriteTimeout: time.Second * 10,
	}

	writer.server = s.Handler.(*websocketServer)

	errc := make(chan error, 1)
	go func() {
		errc <- s.Serve(l)
	}()

	err = <-errc
	logger.Error("failed to serve: %v", zap.Error(err))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	return s.Shutdown(ctx)
}
