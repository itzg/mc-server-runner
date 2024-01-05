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

type Console struct {
	stdInLock  sync.Mutex
	stdInPipe  io.Writer
	stdOutPipe io.Reader
	stdErrPipe io.Reader

	sessionLock    sync.Mutex
	remoteSessions map[uuid.UUID]ssh.Session
}

func makeConsole(stdin io.Writer, stdout io.Reader, stderr io.Reader) Console {
	return Console{
		stdInPipe:      stdin,
		stdOutPipe:     stdout,
		stdErrPipe:     stderr,
		remoteSessions: map[uuid.UUID]ssh.Session{},
	}
}

func (c *Console) WriteToStdIn(p []byte) (n int, err error) {
	c.stdInLock.Lock()
	n, err = c.stdInPipe.Write(p)
	c.stdInLock.Unlock()

	return n, err
}

func (c *Console) RegisterSession(id uuid.UUID, session ssh.Session) {
	c.sessionLock.Lock()
	c.remoteSessions[id] = session
	c.sessionLock.Unlock()
}

func (c *Console) UnregisterSession(id uuid.UUID) {
	c.sessionLock.Lock()
	delete(c.remoteSessions, id)
	c.sessionLock.Unlock()
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

	lines := make(chan string)
	clientExit := make(chan struct{})

	go func() {
		terminal := term.NewTerminal(session, "")
		for {
			line, err := terminal.ReadLine()
			if err != nil {
				if err != io.EOF {
					logger.Error(fmt.Sprintf("Unable to read line from session (%s/%s)", session.User(), session.RemoteAddr().String()), zap.Error(err))
				}
				close(clientExit)
				return
			}

			lines <- line
		}
	}()

	shouldRun := true
	for shouldRun {
		select {
		case line := <-lines:
			lineBytes := []byte(fmt.Sprintf("%s\n", line))
			_, err := console.WriteToStdIn(lineBytes)
			if err != nil {
				logger.Error(fmt.Sprintf("Session failed to write to stdin (%s/%s)", session.User(), session.RemoteAddr().String()), zap.Error(err))
			}
		case <-clientExit:
			shouldRun = false
		case <-session.Context().Done():
			shouldRun = false
		}
	}

	// Tear down the session
	console.UnregisterSession(sessionId)
	logger.Info(fmt.Sprintf("Remote console session disconnected (%s/%s)", session.User(), session.RemoteAddr().String()))
}

// Use os.Stdout for console.
// There should only ever be one of these...
func stdOutRoutine(stdOut io.Writer, console *Console, logger *zap.Logger) {
	scanner := bufio.NewScanner(console.stdOutPipe)
	for scanner.Scan() {
		output := []byte(fmt.Sprintf("%s\n", scanner.Text()))
		_, err := stdOut.Write(output)
		if err != nil {
			logger.Error("Failed to write to stdout")
		}

		for _, session := range console.remoteSessions {
			session.Write(output)
		}
	}
}

// Use os.Stdout for console.
// There should only ever be one of these...
func stdErrRoutine(stdErr io.Writer, console *Console, logger *zap.Logger) {
	scanner := bufio.NewScanner(console.stdErrPipe)
	for scanner.Scan() {
		output := []byte(fmt.Sprintf("%s\n", scanner.Text()))
		_, err := stdErr.Write(output)
		if err != nil {
			logger.Error("Failed to write to stderr")
		}

		for _, session := range console.remoteSessions {
			session.Stderr().Write(output)
		}
	}
}

// Use os.Stdin for console, or pass in an ssh Session to read commands from the remote session.
func stdInRoutine(stdIn io.Reader, console *Console, logger *zap.Logger) {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		text := scanner.Text()
		output := []byte(fmt.Sprintf("%s\n", text))
		_, err := console.WriteToStdIn(output)
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

func startRemoteShellServer(console *Console, logger *zap.Logger) {
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
