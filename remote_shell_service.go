package main

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
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
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

type ConsoleTarget int32

const (
	RSAKeyType string = "RSA PRIVATE KEY"
	ECKeyType         = "EC PRIVATE KEY"
)

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

const (
	// Current filename, hides on Linux systems.
	HostKeyFilename string = ".hostKey.pem"

	// Old filename, not hidden.
	OldHostKeyFilename = "hostKey.pem"
)

// Use the hidden form first, but fallback to the non-hidden one if it already exists.
func pickHostKeyPath(homeDir string) string {
	defaultKeyfilePath := filepath.Join(homeDir, HostKeyFilename)
	_, err := os.Stat(defaultKeyfilePath)
	if !os.IsNotExist(err) {
		return defaultKeyfilePath
	}

	fallbackKeyfilePath := filepath.Join(homeDir, OldHostKeyFilename)
	_, err = os.Stat(fallbackKeyfilePath)
	if !os.IsNotExist(err) {
		return fallbackKeyfilePath
	}

	return defaultKeyfilePath
}

// Exists to clean up the non-hidden key file if it still exists
func cleanupOldHostKey() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	keyfilePath := filepath.Join(homeDir, OldHostKeyFilename)
	_, err = os.Stat(keyfilePath)
	if os.IsNotExist(err) {
		return nil
	}

	err = os.Remove(keyfilePath)
	if err != nil {
		return err
	}

	_, err = os.Stat(keyfilePath)
	if !os.IsNotExist(err) {
		return err
	}

	return nil
}

type hostKeys struct {
	rsaKey *rsa.PrivateKey
	ecKey  *ecdsa.PrivateKey
}

func populateKeys(keys *hostKeys, logger *zap.Logger) (bool, error) {
	didAdd := false
	if keys.ecKey == nil {
		logger.Info("Generating ECDSA SSH Host Key")
		ellipticKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
		if err != nil {
			return didAdd, err
		}

		keys.ecKey = ellipticKey
		didAdd = true
	}

	if keys.rsaKey == nil {
		logger.Info("Generating RSA SSH Host Key")
		rsaKey, err := rsa.GenerateKey(rand.Reader, 4096)
		if err != nil {
			return didAdd, err
		}

		keys.rsaKey = rsaKey
		didAdd = true
	}

	return didAdd, nil
}

func writeKeys(hostKeyPath string, keys *hostKeys, logger *zap.Logger) error {
	keysFile, err := os.OpenFile(hostKeyPath, os.O_CREATE+os.O_WRONLY+os.O_TRUNC, 0600)
	if err != nil {
		return err
	}

	defer keysFile.Close()

	logger.Info(fmt.Sprintf("Writing Host Keys to %s.", hostKeyPath))
	if keys.ecKey != nil {
		ecDER, err := x509.MarshalECPrivateKey(keys.ecKey)
		if err != nil {
			return err
		}

		ecBlock := pem.Block{
			Type:  ECKeyType,
			Bytes: ecDER,
		}

		pem.Encode(keysFile, &ecBlock)
	}

	if keys.rsaKey != nil {
		rsaDER := x509.MarshalPKCS1PrivateKey(keys.rsaKey)
		rsaBlock := pem.Block{
			Type:  RSAKeyType,
			Bytes: rsaDER,
		}

		pem.Encode(keysFile, &rsaBlock)
	}

	return nil
}

func readKeys(hostKeyPath string) (*hostKeys, error) {
	bytes, err := os.ReadFile(hostKeyPath)
	if err != nil {
		return nil, err
	}

	var keys hostKeys
	for len(bytes) > 0 {
		pemBlock, next := pem.Decode(bytes)
		if pemBlock == nil {
			break
		}

		switch pemBlock.Type {
		case RSAKeyType:
			rsaKey, err := x509.ParsePKCS1PrivateKey(pemBlock.Bytes)
			if err != nil {
				return &keys, err
			}
			keys.rsaKey = rsaKey
		case ECKeyType:
			ecKey, err := x509.ParseECPrivateKey(pemBlock.Bytes)
			if err != nil {
				return &keys, err
			}
			keys.ecKey = ecKey
		}

		bytes = next
	}

	return &keys, nil
}

func ensureHostKeys(logger *zap.Logger) (*hostKeys, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	keyfilePath := pickHostKeyPath(homeDir)
	defaultKeyfilePath := filepath.Join(homeDir, HostKeyFilename)
	fileChanged := keyfilePath != defaultKeyfilePath
	_, err = os.Stat(keyfilePath)
	if os.IsNotExist(err) {
		logger.Info("Generating host keys for remote shell server.")
		var hostKeys hostKeys
		addedKeys, err := populateKeys(&hostKeys, logger)

		if (fileChanged || addedKeys) && err == nil {
			writeKeys(defaultKeyfilePath, &hostKeys, logger)
		}
		return &hostKeys, err
	} else {
		logger.Info(fmt.Sprintf("Reading host keys for remote shell from %s.", keyfilePath))
		hostKeys, err := readKeys(keyfilePath)
		if err != nil {
			return nil, err
		}

		// Populate missing keys (older files only have RSA)
		addedKeys, err := populateKeys(hostKeys, logger)

		if (fileChanged || addedKeys) && err == nil {
			writeKeys(defaultKeyfilePath, hostKeys, logger)
		}
		return hostKeys, err
	}
}

func twinKeys(keys *hostKeys) ssh.Option {
	return func(srv *ssh.Server) error {
		rsaSigner, err := gossh.NewSignerFromKey(keys.rsaKey)
		if err != nil {
			return err
		}
		srv.AddHostKey(rsaSigner)

		ecSigner, err := gossh.NewSignerFromKey(keys.ecKey)
		if err != nil {
			return err
		}
		srv.AddHostKey(ecSigner)

		return nil
	}
}

func runRemoteShellServer(console *Console, logger *zap.Logger) {
	logger.Info("Starting remote shell server on 2222...")
	ssh.Handle(func(s ssh.Session) { handleSession(s, console, logger) })

	hostKeys, err := ensureHostKeys(logger)
	if err != nil {
		logger.Error("Unable to ensure host keys exist", zap.Error(err))
		return
	}

	err = cleanupOldHostKey()
	if err != nil {
		logger.Warn("Unable to remote old host key file", zap.Error(err))
	}

	log.Fatal(ssh.ListenAndServe(
		":2222",
		nil,
		twinKeys(hostKeys),
		ssh.PasswordAuth(func(ctx ssh.Context, password string) bool { return passwordHandler(ctx, password, logger) }),
	))
}
