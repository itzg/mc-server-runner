package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
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
	MessageTypeStdout    MessageType = "stdout"
	MessageTypeStderr    MessageType = "stderr"
	MessageTypeWelcome   MessageType = "welcome"
	MessageTypeAuthError MessageType = "auth_err"
)

type Message interface {
	GetType() string
}

type StdinMessage struct {
	Type    MessageType `json:"type"`
	Content string      `json:"content"`
}

func (m StdinMessage) GetType() string { return string(m.Type) }

type StdoutMessage struct {
	Type    MessageType `json:"type"`
	Content string      `json:"content"`
	Time    time.Time   `json:"time,omitzero"`
}

func (m StdoutMessage) GetType() string { return string(m.Type) }

type StderrMessage struct {
	Type    MessageType `json:"type"`
	Content string      `json:"content"`
	Time    time.Time   `json:"time,omitzero"`
}

func (m StderrMessage) GetType() string { return string(m.Type) }

type WelcomeMessage struct {
	Type        MessageType `json:"type"`
	RecentLines []string    `json:"RecentLines"`
}

func (m WelcomeMessage) GetType() string { return string(m.Type) }

type AuthErrorMessage struct {
	Type   MessageType `json:"type"`
	Reason string      `json:"reason"`
}

func (m AuthErrorMessage) GetType() string { return string(m.Type) }

type WsClient struct {
	wsConn         *websocket.Conn
	responseWriter http.ResponseWriter
	request        http.Request
	writeMutex     sync.Mutex
}

type websocketServer struct {
	logger      *zap.Logger
	stdin       io.Writer
	clients     map[uuid.UUID]*WsClient
	mu          sync.Mutex
	disableAuth bool
}

func getWebsocketPassword() string {
	var password string

	password = os.Getenv("WEBSOCKET_PASSWORD")
	if password != "" {
		return password
	}

	password = os.Getenv("RCON_PASSWORD")
	if password != "" {
		return password
	}

	return "minecraft"
}

func (s *websocketServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.disableAuth {
		password := r.Header.Get("X-WS-Auth")
		if password != getWebsocketPassword() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)

			errMsg := AuthErrorMessage{
				Type:   MessageTypeAuthError,
				Reason: "invalid password",
			}
			json.NewEncoder(w).Encode(errMsg)
			return
		}
	}

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

	for {
		err = echo(c, s, ctx)
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
			pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)

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

func echo(c *websocket.Conn, s *websocketServer, ctx context.Context) error {
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

			s.logger.Debug(fmt.Sprintf("Received raw data: %q\n", string(data)))

			var msg StdinMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				s.logger.Error(fmt.Sprintf("JSON parse error: %v\n", err))
			} else {
				s.logger.Debug(fmt.Sprintf("Successfully parsed JSON: %+v\n", msg))
				content := msg.Content
				if !strings.HasSuffix(content, "\n") {
					content += "\n"
				}
				s.stdin.Write([]byte(content))
			}
		}
	}
}

type wsWriter struct {
	server *websocketServer
}

func (b *wsWriter) Write(p []byte) (int, error) {
	msg := string(p)
	if b.server != nil {
		b.server.broadcast(msg)
	}
	return len(p), nil
}

func (s *websocketServer) broadcast(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for id, client := range s.clients {
		client.writeMutex.Lock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		message := &StdoutMessage{
			Type:    MessageTypeStdout,
			Content: string([]byte(msg)),
			Time:    time.Now(),
		}
		err := wsjson.Write(ctx, client.wsConn, message)
		cancel()
		client.writeMutex.Unlock()

		if err != nil {
			s.logger.Error("failed to send message to client",
				zap.String("client", id.String()),
				zap.Error(err),
			)
			client.wsConn.Close(websocket.StatusInternalError, "closing client")
			delete(s.clients, id)
		}
	}
}

func runWebsocketServer(ctx context.Context, logger *zap.Logger, errorChan chan error, finished *sync.WaitGroup, stdoutWriter *wsWriter, stderrWriter *wsWriter, stdin io.Writer, disableAuth bool, address string) {
	l, err := net.Listen("tcp", address)
	if err != nil {
		errorChan <- fmt.Errorf("failed to setup websocket server on %s: %w", address, err)
		return
	}
	logger.Info(fmt.Sprintf("Starting websocket on ws://%v", l.Addr()))

	s := &http.Server{
		Handler: &websocketServer{
			logger,
			stdin,
			map[uuid.UUID]*WsClient{},
			sync.Mutex{},
			disableAuth,
		},
		ReadTimeout:  time.Second * 10,
		WriteTimeout: time.Second * 10,
	}

	stdoutWriter.server = s.Handler.(*websocketServer)
	stderrWriter.server = s.Handler.(*websocketServer)

	go func() {
		serveErr := s.Serve(l)
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errorChan <- fmt.Errorf("failed to serve websocket server: %w", serveErr)
		}

		finished.Done()
	}()

	defer func() {
		timedCtx, cancel := context.WithTimeout(context.Background(), time.Second*10)
		defer cancel()

		logger.Debug("shutting down websocket server")
		shutdownErr := s.Shutdown(timedCtx)
		if shutdownErr != nil {
			logger.Error("failed to shutdown server", zap.Error(shutdownErr))
		}
	}()

	<-ctx.Done()
}
