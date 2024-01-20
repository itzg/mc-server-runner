package main

import (
	"bufio"
	"crypto/rand"
	"crypto/rsa"
	"crypto/subtle"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/gliderlabs/ssh"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"golang.org/x/term"
)

type ConsoleTarget int32

const (
	stdOutTarget ConsoleTarget = 0
	stdErrTarget ConsoleTarget = 1
)

type Console struct {
	stdInLock  sync.Mutex
	stdInPipe  io.Writer
	stdOutPipe io.Reader
	stdErrPipe io.Reader

	sessionLock    sync.Mutex
	remoteSessions map[uuid.UUID]ssh.Session
}

func makeConsole(stdin io.Writer, stdout io.Reader, stderr io.Reader) *Console {
	return &Console{
		stdInPipe:      stdin,
		stdOutPipe:     stdout,
		stdErrPipe:     stderr,
		remoteSessions: map[uuid.UUID]ssh.Session{},
	}
}

func (c *Console) OutputPipe(target ConsoleTarget) io.Reader {
	switch target {
	case stdOutTarget:
		return c.stdOutPipe
	case stdErrTarget:
		return c.stdErrPipe
	default:
		return c.stdOutPipe
	}
}

// Safely write to server's stdin
func (c *Console) WriteToStdIn(p []byte) (n int, err error) {
	c.stdInLock.Lock()
	n, err = c.stdInPipe.Write(p)
	c.stdInLock.Unlock()

	return n, err
}

// Register a remote console session for output
func (c *Console) RegisterSession(id uuid.UUID, session ssh.Session) {
	c.sessionLock.Lock()
	c.remoteSessions[id] = session
	c.sessionLock.Unlock()
}

// Deregister a remote console session
func (c *Console) UnregisterSession(id uuid.UUID) {
	c.sessionLock.Lock()
	delete(c.remoteSessions, id)
	c.sessionLock.Unlock()
}

// Fetch current sessions in a thread-safe way
func (c *Console) CurrentSessions() []ssh.Session {
	c.sessionLock.Lock()
	values := []ssh.Session{}
	for _, value := range c.remoteSessions {
		values = append(values, value)
	}
	c.sessionLock.Unlock()

	return values
}

func passwordHandler(ctx ssh.Context, password string, logger *zap.Logger) bool {
	expectedPassword := os.Getenv("RCON_PASSWORD")
	if expectedPassword == "" {
		expectedPassword = "minecraft"
	}

	lengthComp := subtle.ConstantTimeEq(int32(len(password)), int32(len(expectedPassword)))
	contentComp := subtle.ConstantTimeCompare([]byte(password), []byte(expectedPassword))
	isValid := lengthComp == 1 && contentComp == 1
	if !isValid {
		logger.Warn(fmt.Sprintf("Remote console session rejected (%s/%s)", ctx.User(), ctx.RemoteAddr().String()))
	}
	return isValid
}

func handleSession(session ssh.Session, console *Console, logger *zap.Logger) {
	// Setup state for the console session
	sessionId := uuid.New()
	_, _, isTty := session.Pty()
	logger.Info(fmt.Sprintf("Remote console session accepted (%s/%s) isTTY: %t", session.User(), session.RemoteAddr().String(), isTty))
	console.RegisterSession(sessionId, session)

	// Wrap the session in a terminal so we can read lines.
	// Individual lines will be sent to the input channel to be processed as commands for the server.
	// If the user sends Ctrl-C/D, this shows up as an EOF and will close the channel.
	input := make(chan string)
	go func() {
		terminal := term.NewTerminal(session, "")
		for {
			line, err := terminal.ReadLine()
			if err != nil {
				// Check for client-triggered (expected) exit before logging as an error.
				if err != io.EOF {
					logger.Error(fmt.Sprintf("Unable to read line from session (%s/%s)", session.User(), session.RemoteAddr().String()), zap.Error(err))
				}
				close(input)
				return
			}

			input <- line
		}
	}()

InputLoop:
	for {
		select {
		case line, ok := <-input:
			if !ok {
				break InputLoop
			}

			lineBytes := []byte(fmt.Sprintf("%s\n", line))
			_, err := console.WriteToStdIn(lineBytes)
			if err != nil {
				logger.Error(fmt.Sprintf("Session failed to write to stdin (%s/%s)", session.User(), session.RemoteAddr().String()), zap.Error(err))
			}
		case <-session.Context().Done():
			break InputLoop
		}
	}

	// Tear down the session
	console.UnregisterSession(sessionId)
	logger.Info(fmt.Sprintf("Remote console session disconnected (%s/%s)", session.User(), session.RemoteAddr().String()))
}

// Use stdOut or stdErr for output.
// There should only ever be one at a time per pipe
func consoleOutRoutine(output io.Writer, console *Console, target ConsoleTarget, logger *zap.Logger) {
	scanner := bufio.NewScanner(console.OutputPipe(target))
	for scanner.Scan() {
		outBytes := []byte(fmt.Sprintf("%s\n", scanner.Text()))
		_, err := output.Write(outBytes)
		if err != nil {
			logger.Error("Failed to write to stdout")
		}

		remoteSessions := console.CurrentSessions()
		for _, session := range remoteSessions {
			switch target {
			case stdOutTarget:
				session.Write(outBytes)
			case stdErrTarget:
				session.Stderr().Write(outBytes)
			}
		}
	}
}

// Use os.Stdin for console.
func consoleInRoutine(stdIn io.Reader, console *Console, logger *zap.Logger) {
	scanner := bufio.NewScanner(stdIn)
	for scanner.Scan() {
		text := scanner.Text()
		outBytes := []byte(fmt.Sprintf("%s\n", text))
		_, err := console.WriteToStdIn(outBytes)
		if err != nil {
			logger.Error("Failed to write to stdin")
		}
	}
}

func ensureHostKey(logger *zap.Logger) (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	keyfilePath := filepath.Join(homeDir, "hostKey.pem")
	_, err = os.Stat(keyfilePath)
	if os.IsNotExist(err) {
		logger.Info("Generating host key for remote shell server.")
		hostKey, err := rsa.GenerateKey(rand.Reader, 4096)
		if err != nil {
			return keyfilePath, err
		}

		err = hostKey.Validate()
		if err != nil {
			return keyfilePath, err
		}

		hostDER := x509.MarshalPKCS1PrivateKey(hostKey)
		hostBlock := pem.Block{
			Type:    "RSA PRIVATE KEY",
			Headers: nil,
			Bytes:   hostDER,
		}
		hostPEM := pem.EncodeToMemory(&hostBlock)

		err = os.WriteFile(keyfilePath, hostPEM, 0600)
		return keyfilePath, err
	}

	return keyfilePath, err
}

func runRemoteShellServer(console *Console, logger *zap.Logger) {
	logger.Info("Starting remote shell server on 2222...")
	ssh.Handle(func(s ssh.Session) { handleSession(s, console, logger) })

	hostKeyPath, err := ensureHostKey(logger)
	if err != nil {
		logger.Error("Unable to ensure host key exists", zap.Error(err))
		return
	}

	log.Fatal(ssh.ListenAndServe(
		":2222",
		nil,
		ssh.HostKeyFile(hostKeyPath),
		ssh.PasswordAuth(func(ctx ssh.Context, password string) bool { return passwordHandler(ctx, password, logger) }),
	))
}
