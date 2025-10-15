package main

import (
	"container/ring"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
	"go.uber.org/zap"
	// "golang.org/x/time/rate"
)

type messageType string

const (
	// Client -> Server
	MessageTypeStdin messageType = "stdin"
	// Server -> Client
	MessageTypeStdout    messageType = "stdout"
	MessageTypeStderr    messageType = "stderr"
	MessageTypeWelcome   messageType = "welcome"
	MessageTypeAuthError messageType = "auth_err"
)

type wsMessage interface {
	getType() string
}

type stdinMessage struct {
	Type    messageType `json:"type"`
	Content string      `json:"content"`
}

func (m stdinMessage) getType() string { return string(m.Type) }

type stdoutMessage struct {
	Type    messageType `json:"type"`
	Content string      `json:"content"`
	Time    time.Time   `json:"time,omitzero"`
}

func (m stdoutMessage) getType() string { return string(m.Type) }

type stderrMessage struct {
	Type    messageType `json:"type"`
	Content string      `json:"content"`
	Time    time.Time   `json:"time,omitzero"`
}

func (m stderrMessage) getType() string { return string(m.Type) }

type welcomeMessage struct {
	Type        messageType `json:"type"`
	RecentLines []string    `json:"recentLines"`
}

func (m welcomeMessage) getType() string { return string(m.Type) }

type authErrorMessage struct {
	Type   messageType `json:"type"`
	Reason string      `json:"reason"`
}

func (m authErrorMessage) getType() string { return string(m.Type) }

type wsClient struct {
	wsConn         *websocket.Conn
	responseWriter http.ResponseWriter
	request        http.Request
	writeMutex     sync.Mutex
}

type websocketServer struct {
	logger             *zap.Logger
	stdin              io.Writer
	clients            map[uuid.UUID]*wsClient
	mu                 sync.Mutex
	disableAuth        bool
	trustedOrigins     []string
	disableOriginCheck bool
}

type logRing struct {
	r  *ring.Ring
	mu sync.RWMutex
}

func newLogRing(logBufferSize int) *logRing {
	return &logRing{
		r: ring.New(logBufferSize),
	}
}

func (lr *logRing) add(s string) {
	lr.mu.Lock()
	defer lr.mu.Unlock()

	lr.r = lr.r.Next()
	lr.r.Value = s
}

func (lr *logRing) getAll() []string {
	lr.mu.RLock()
	defer lr.mu.RUnlock()

	var result []string

	startNode := lr.r.Next()

	startNode.Do(func(v any) {
		if v != nil {
			result = append(result, v.(string))
		}
	})

	return result
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

func extractAuthTokenFromProtocols(header http.Header, expectedProto string) (string, bool) {
	protoHeader := header.Get("Sec-WebSocket-Protocol")
	if protoHeader == "" {
		return "", false
	}

	protocols := strings.Split(protoHeader, ",")

	for i, p := range protocols {
		tp := strings.TrimSpace(p)

		if tp == expectedProto {
			if i+1 > len(protocols) {
				return "", false
			}
			token := strings.TrimSpace(protocols[i+1])

			if token != "" {
				return token, true
			}
		}
	}
	return "", false
}

func isOriginAllowed(origin string, trustedOrigins []string) bool {
	return slices.Contains(trustedOrigins, origin)
}

var logHistory *logRing

func (s *websocketServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.disableOriginCheck {
		origin := r.Header.Get("Origin")

		if !isOriginAllowed(origin, s.trustedOrigins) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)

			errMsg := authErrorMessage{
				Type:   MessageTypeAuthError,
				Reason: "origin not allowed",
			}
			json.NewEncoder(w).Encode(errMsg)
			s.logger.Info(
				"Websocket connection rejected",
				zap.String("addr", r.RemoteAddr),
				zap.String("reason", "origin not allowed"),
			)
			return
		}
	}

	if !s.disableAuth {
		// Authentication header should be extracted here. This is similar to how Minecraft's JSON-RPC over Websocket API works.
		// expect string: "mc-server-runner-ws-v1, <TOKEN HERE>"
		token, exists := extractAuthTokenFromProtocols(r.Header, "mc-server-runner-ws-v1")

		if !exists || token != getWebsocketPassword() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)

			errMsg := authErrorMessage{
				Type:   MessageTypeAuthError,
				Reason: "invalid password",
			}
			json.NewEncoder(w).Encode(errMsg)
			s.logger.Info(
				"Websocket connection rejected",
				zap.String("addr", r.RemoteAddr),
				zap.String("reason", "invalid password"),
			)
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
	s.clients[sessionId] = &wsClient{
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

	wsjson.Write(ctx, c, welcomeMessage{
		Type:        MessageTypeWelcome,
		RecentLines: logHistory.getAll(),
	})

	for {
		if err = handleIncoming(c, s, ctx); err != nil {
			s.logger.Debug("closing websocket session", zap.String("sessionId", sessionId.String()))
			delete(s.clients, sessionId)

			closeStatus := websocket.CloseStatus(err)
			switch closeStatus {
			case websocket.StatusNormalClosure | websocket.StatusGoingAway:
				s.logger.Info(
					fmt.Sprintf("Websocket connection closed with %v", r.RemoteAddr),
					zap.Uint("code", uint(closeStatus)),
					zap.String("reason", closeStatus.String()),
				)
				return
			default:
				s.logger.Error(
					fmt.Sprintf("failed to echo with %v", r.RemoteAddr),
					zap.Uint("code", uint(closeStatus)),
					zap.String("reason", closeStatus.String()),
					zap.Error(err),
				)
				return
			}
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

func handleIncoming(c *websocket.Conn, s *websocketServer, ctx context.Context) error {
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

			var msg stdinMessage
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
	writerType messageType
	server     *websocketServer
}

func (b *wsWriter) Write(p []byte) (int, error) {
	msg := string(p)
	if b.server != nil {
		b.server.broadcast(msg, b.writerType)
		logHistory.add(msg)
	}
	return len(p), nil
}

func (s *websocketServer) broadcast(msg string, msgType messageType) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for id, client := range s.clients {
		client.writeMutex.Lock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var message wsMessage
		switch msgType {
		case MessageTypeStdout:
			message = &stdoutMessage{
				Type:    MessageTypeStdout,
				Content: string([]byte(msg)),
				Time:    time.Now(),
			}
		case MessageTypeStderr:
			message = &stderrMessage{
				Type:    MessageTypeStderr,
				Content: string([]byte(msg)),
				Time:    time.Now(),
			}
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

func runWebsocketServer(ctx context.Context, logger *zap.Logger, errorChan chan error, finished *sync.WaitGroup, stdoutWriter *wsWriter, stderrWriter *wsWriter, stdin io.Writer, disableAuth bool, address string, trustedOrigins []string, disableOriginCheck bool, logBufferSize int) {
	l, err := net.Listen("tcp", address)
	if err != nil {
		errorChan <- fmt.Errorf("failed to setup websocket server on %s: %w", address, err)
		return
	}
	logHistory = newLogRing(int(logBufferSize))
	logger.Info(fmt.Sprintf("Starting websocket server on ws://%v", l.Addr()))
	if disableAuth {
		logger.Warn("Websocket authentication is DISABLED. The websocket endpoint is unprotected and will accept commands from any client. This is insecure and not recommended for production.")
	}
	if disableOriginCheck {
		logger.Warn("Origin check is DISABLED. The server will accept connections from browsers on ANY website, making it vulnerable to Cross-Site WebSocket Hijacking (CSWSH).")
	}

	s := &http.Server{
		Handler: &websocketServer{
			logger,
			stdin,
			map[uuid.UUID]*wsClient{},
			sync.Mutex{},
			disableAuth,
			trustedOrigins,
			disableOriginCheck,
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
