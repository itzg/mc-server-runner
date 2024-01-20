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
	StopDuration            time.Duration `usage:"Amount of time in Golang duration to wait after sending the 'stop' command."`
	StopServerAnnounceDelay time.Duration `default:"0s" usage:"Amount of time in Golang duration to wait after announcing server shutdown"`
	DetachStdin             bool          `usage:"Don't forward stdin and allow process to be put in background"`
	RemoteConsole           bool          `usage:"Allow remote shell connections over SSH to server console"`
	Shell                   string        `usage:"When set, pass the arguments to this shell"`
	NamedPipe               string        `usage:"Optional path to create and read a named pipe for console input"`
}

func main() {
	signalChan := make(chan os.Signal, 1)
	// docker stop sends a SIGTERM, so intercept that and send a 'stop' command to the server
	signal.Notify(signalChan, syscall.SIGTERM)

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

	if args.RemoteConsole {
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			logger.Error("Unable to get stdout", zap.Error(err))
		}

		stderr, err := cmd.StderrPipe()
		if err != nil {
			logger.Error("Unable to get stderr", zap.Error(err))
		}

		console := makeConsole(stdin, stdout, stderr)

		// Relay stdin between outside and server
		if !args.DetachStdin {
			go consoleInRoutine(os.Stdin, console, logger)
		}

		go consoleOutRoutine(os.Stdout, console, stdOutTarget, logger)
		go consoleOutRoutine(os.Stderr, console, stdErrTarget, logger)

		go runRemoteShellServer(console, logger)

		logger.Info("Running with remote console support")
	} else {
		logger.Debug("Directly assigning stdout/stderr")
		// directly assign stdout/err to pass through terminal, if applicable
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if hasRconCli() {
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

	for {
		select {
		case <-signalChan:
			if args.StopServerAnnounceDelay > 0 {
				announceStopViaConsole(logger, stdin, args.StopServerAnnounceDelay)
				logger.Info("Sleeping before server stop", zap.Duration("sleepTime", args.StopServerAnnounceDelay))
				time.Sleep(args.StopServerAnnounceDelay)
			}

			if hasRconCli() {
				err := stopWithRconCli()
				if err != nil {
					logger.Error("Failed to stop using rcon-cli", zap.Error(err))
					stopViaConsole(logger, stdin)
				}
			} else {
				stopViaConsole(logger, stdin)
			}

			logger.Info("Waiting for completion...")
			if args.StopDuration != 0 {
				time.AfterFunc(args.StopDuration, func() {
					logger.Error("Took too long, so killing server process")
					err := cmd.Process.Kill()
					if err != nil {
						logger.Error("Failed to forcefully kill process")
					}
				})
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

func stopWithRconCli() error {
	log.Println("Stopping with rcon-cli")

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

		rconCliCmd := exec.Command("rcon-cli",
			"--port", port,
			"--password", password,
			"stop")

		return rconCliCmd.Run()
	} else {
		rconCliCmd := exec.Command("rcon-cli",
			"--config", rconConfigFile,
			"stop")

		return rconCliCmd.Run()
	}
}

func announceStopViaConsole(logger *zap.Logger, stdin io.Writer, shutdownDelay time.Duration) {
	logger.Info("Sending shutdown announce 'say' to Minecraft server")
	_, err := stdin.Write([]byte(fmt.Sprintf("say Server shutting down in %0.f seconds\n", shutdownDelay.Seconds())))
	if err != nil {
		logger.Error("Failed to write say command to server console", zap.Error(err))
	}
}

func stopViaConsole(logger *zap.Logger, stdin io.Writer) {
	logger.Info("Sending 'stop' to Minecraft server...")
	_, err := stdin.Write([]byte("stop\n"))
	if err != nil {
		logger.Error("Failed to write stop command to server console", zap.Error(err))
	}
}
