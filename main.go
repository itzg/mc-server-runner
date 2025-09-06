package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/itzg/go-flagsfiller"
	"github.com/itzg/zapconfigs"
	"go.uber.org/zap"
)

type Args struct {
	Debug                   bool          `usage:"Enable debug logging"`
	Bootstrap               string        `usage:"Specifies a file with commands to initially send to the server"`
	StopCommand             string        `default:"stop" usage:"Which command to send to the server to stop it"`
	StopDuration            time.Duration `usage:"Amount of time in Golang duration to wait after sending the 'stop' command."`
	StopServerAnnounceDelay time.Duration `default:"0s" usage:"Amount of time in Golang duration to wait after announcing server shutdown"`
	DetachStdin             bool          `usage:"Don't forward stdin and allow process to be put in background"`
	RemoteConsole           bool          `usage:"Allow remote shell connections over SSH to server console"`
	Shell                   string        `usage:"When set, pass the arguments to this shell"`
	NamedPipe               string        `usage:"Optional path to create and read a named pipe for console input"`
	WebsocketConsole        bool          `usage:"Allow remote shell over websocket"`
}

func main() {
	// docker stop sends a SIGTERM, so intercept that and send a 'stop' command to the server
	termChan := make(chan os.Signal, 1)
	signal.Notify(termChan, syscall.SIGTERM)

	// Additionally intercept SIGUSR1 which bypasses StopServerAnnounceDelay (in cases where SIGTERM has **and** hasn't been sent)
	usr1Chan := make(chan os.Signal, 1)
	signal.Notify(usr1Chan, syscall.SIGUSR1)

	var args Args
	err := flagsfiller.Parse(&args)
	if err != nil {
		log.Fatal(err)
	}

	var logger *zap.Logger
	if args.Debug {
		logger = zapconfigs.NewDebugLogger()
	} else {
		logger = zapconfigs.NewDefaultLogger()
	}
	//goland:noinspection GoUnhandledErrorResult
	defer logger.Sync()
	logger = logger.Named("mc-server-runner")

	var cmd *exec.Cmd

	if flag.NArg() < 1 {
		logger.Fatal("Missing executable arguments")
	}

	if args.Shell != "" {
		cmd = exec.Command(args.Shell, flag.Args()...)
	} else {
		cmd = exec.Command(flag.Arg(0), flag.Args()[1:]...)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		logger.Error("Unable to get stdin", zap.Error(err))
	}

	type writers []io.Writer
	var stdoutWritersList writers
	var stderrWritersList writers
	stdoutWritersList = append(stdoutWritersList, os.Stdout)
	stderrWritersList = append(stderrWritersList, os.Stderr)

	if args.WebsocketConsole {
		wsOutWriter := &wsWriter{prefix: ""}
		wsErrWriter := &wsWriter{prefix: "[stderr] "}

		stdoutWritersList = append(stdoutWritersList, wsOutWriter)
		stderrWritersList = append(stderrWritersList, wsErrWriter)

		go runWebsocketServer(logger, wsOutWriter, wsErrWriter, stdin)
	}

	if args.RemoteConsole {
		sshStdoutPipe := newPipeWriter()
		sshStderrPipe := newPipeWriter()

		stdoutWritersList = append(stdoutWritersList, sshStdoutPipe)
		stderrWritersList = append(stderrWritersList, sshStderrPipe)

		// Create readers for the console
		sshStdoutReader := sshStdoutPipe.AddReader()
		sshStderrReader := sshStderrPipe.AddReader()

		console := makeConsole(stdin, sshStdoutReader, sshStderrReader)

		// Relay stdin between outside and server
		if !args.DetachStdin {
			go consoleInRoutine(os.Stdin, console, logger)
		}

		go consoleOutRoutine(os.Stdout, console, stdOutTarget, logger)
		go consoleOutRoutine(os.Stderr, console, stdErrTarget, logger)

		go runRemoteShellServer(console, logger)

		logger.Info("Running with remote console support")
	}

	logger.Debug("Directly assigning stdout/stderr")

	multiOut := io.MultiWriter(stdoutWritersList...)
	multiErr := io.MultiWriter(stderrWritersList...)

	cmd.Stdout = multiOut
	cmd.Stderr = multiErr

	if !args.RemoteConsole {
		if hasRconCli() && args.NamedPipe == "" {
			logger.Debug("Directly assigning stdin")
			cmd.Stdin = os.Stdin
			stdin = os.Stdin
		} else {
			go relayStdin(logger, stdin)
		}
	}

	err = cmd.Start()
	if err != nil {
		logger.Error("Failed to start", zap.Error(err))
	}

	if args.Bootstrap != "" {
		bootstrapContent, err := os.ReadFile(args.Bootstrap)
		if err != nil {
			logger.Error("Failed to read bootstrap commands", zap.Error(err))
		}
		_, err = stdin.Write(bootstrapContent)
		if err != nil {
			logger.Error("Failed to write bootstrap content", zap.Error(err))
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	errorChan := make(chan error, 1)

	if args.NamedPipe != "" {
		err2 := handleNamedPipe(ctx, args.NamedPipe, stdin, errorChan)
		if err2 != nil {
			logger.Fatal("Failed to setup named pipe", zap.Error(err2))
		}
	}

	cmdExitChan := make(chan int, 1)

	go func() {
		waitErr := cmd.Wait()
		if waitErr != nil {
			var exitErr *exec.ExitError
			if errors.As(waitErr, &exitErr) {
				exitCode := exitErr.ExitCode()
				logger.Warn("Minecraft server failed. Inspect logs above for errors that indicate cause. DO NOT report this line as an error.",
					zap.Int("exitCode", exitCode))
				cmdExitChan <- exitCode
			}
			return
		} else {
			cmdExitChan <- 0
		}
	}()

	var timer *time.Timer

	for {
		select {
		case <-termChan:
			logger.Debug("SIGTERM caught")
			logger.Info("gracefully stopping server...")
			if args.StopServerAnnounceDelay > 0 {
				announceStop(logger, stdin, args.StopServerAnnounceDelay)
				logger.Info("Sleeping before server stop", zap.Duration("sleepTime", args.StopServerAnnounceDelay))
				timer = time.AfterFunc(args.StopServerAnnounceDelay, func() {
					logger.Info("StopServerAnnounceDelay elapsed, stopping server")
					terminate(logger, stdin, cmd, args.StopDuration, args.StopCommand)
				})
			} else {
				terminate(logger, stdin, cmd, args.StopDuration, args.StopCommand)
			}

		case <-usr1Chan:
			if timer != nil {
				if timer.Stop() {
					logger.Info("SIGUSR1 caught, bypassing running StopServerAnnounceDelay")
					terminate(logger, stdin, cmd, args.StopDuration, args.StopCommand)
				} else {
					logger.Info("SIGUSR1 caught, StopServerAnnounceDelay already elapsed, server is already stopping")
				}
			} else {
				logger.Info("SIGUSR1 caught, gracefully stopping server... (without StopServerAnnounceDelay)")
				terminate(logger, stdin, cmd, args.StopDuration, args.StopCommand)
			}

		case namedPipeErr := <-errorChan:
			logger.Error("Error during named pipe handling", zap.Error(namedPipeErr))

		case exitCode := <-cmdExitChan:
			cancel()
			logger.Info("Done")
			os.Exit(exitCode)
		}
	}

}

func relayStdin(logger *zap.Logger, stdin io.WriteCloser) {
	_, err := io.Copy(stdin, os.Stdin)
	if err != nil {
		logger.Error("Failed to relay standard input", zap.Error(err))
	}
}

func hasRconCli() bool {
	if strings.ToUpper(os.Getenv("ENABLE_RCON")) == "TRUE" {
		_, err := exec.LookPath("rcon-cli")
		return err == nil
	} else {
		return false
	}
}

func sendRconCommand(cmd ...string) error {
	rconConfigFile := os.Getenv("RCON_CONFIG_FILE")
	if rconConfigFile == "" {
		port := os.Getenv("RCON_PORT")
		if port == "" {
			port = "25575"
		}

		password := os.Getenv("RCON_PASSWORD")
		if password == "" {
			password = "minecraft"
		}

		args := []string{"--port", port,
			"--password", password}
		args = append(args, cmd...)

		rconCliCmd := exec.Command("rcon-cli", args...)

		return rconCliCmd.Run()
	} else {

		args := []string{"--config", rconConfigFile}
		args = append(args, cmd...)

		rconCliCmd := exec.Command("rcon-cli", args...)

		return rconCliCmd.Run()
	}
}

// sendCommand will send the given command via RCON when available, otherwise it will write to the given stdin
func sendCommand(stdin io.Writer, cmd ...string) error {
	if hasRconCli() {
		return sendRconCommand(cmd...)
	} else {
		_, err := stdin.Write([]byte(strings.Join(cmd, " ")))
		return err
	}
}

// terminate sends `stop` to the server and kill process once stopDuration elapsed
func terminate(logger *zap.Logger, stdin io.Writer, cmd *exec.Cmd, stopDuration time.Duration, stopCommand string) {
	if stopCommand == "" {
		stopCommand = "stop"
	}
	if hasRconCli() {
		err := stopWithRconCli(stopCommand)
		if err != nil {
			logger.Error("Failed to stop using rcon-cli", zap.Error(err))
			stopViaConsole(logger, stdin, stopCommand)
		}
	} else {
		stopViaConsole(logger, stdin, stopCommand)
	}

	logger.Info("Waiting for completion...")
	if stopDuration != 0 {
		time.AfterFunc(stopDuration, func() {
			logger.Error("Took too long, so killing server process")
			err := cmd.Process.Kill()
			if err != nil {
				logger.Error("Failed to forcefully kill process")
			}
		})
	}
}

func announceStop(logger *zap.Logger, stdin io.Writer, shutdownDelay time.Duration) {
	logger.Info("Sending shutdown announce 'say' to Minecraft server")

	err := sendCommand(stdin, "say", fmt.Sprintf("Server shutting down in %0.f seconds", shutdownDelay.Seconds()))
	if err != nil {
		logger.Error("Failed to send 'say' command", zap.Error(err))
	}
}

func stopWithRconCli(stopCommand string) error {
	log.Println("Stopping with rcon-cli")

	return sendRconCommand(stopCommand)
}

func stopViaConsole(logger *zap.Logger, stdin io.Writer, stopCommand string) {
	logger.Info("Sending '" + stopCommand + "' to Minecraft server...")
	_, err := stdin.Write([]byte(stopCommand + "\n"))
	if err != nil {
		logger.Error("Failed to write stop command to server console", zap.Error(err))
	}
}
