package main

import (
	"flag"
	"fmt"
	"github.com/itzg/go-flagsfiller"
	"github.com/itzg/mc-server-runner/cfsync"
	"github.com/itzg/zapconfigs"
	"go.uber.org/zap"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

type Args struct {
	Debug                   bool          `usage:"Enable debug logging"`
	Bootstrap               string        `usage:"Specifies a file with commands to initially send to the server"`
	StopDuration            time.Duration `usage:"Amount of time in Golang duration to wait after sending the 'stop' command."`
	StopServerAnnounceDelay time.Duration `default:"0s" usage:"Amount of time in Golang duration to wait after announcing server shutdown"`
	DetachStdin             bool          `usage:"Don't forward stdin and allow process to be put in background"`
	Shell                   string        `usage:"When set, pass the arguments to this shell"`
	Cf                      struct {
		InstanceFile string `usage:"Path to a Twitch/Curse minecraftinstance.json file for server setup"`
	}
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
	defer logger.Sync()
	logger = logger.Named("mc-server-runner")

	var cmd *exec.Cmd

	if flag.NArg() < 1 {
		logger.Fatal("Missing executable arguments")
	}

	if args.Cf.InstanceFile != "" {
		serverJar, err := cfsync.PrepareInstanceFromFile(logger.Named("cfsync"), args.Cf.InstanceFile, ".")
		if err != nil {
			logger.Fatal("Failed to prepare instance", zap.Error(err))
		}

		args, err := fillServerJar(flag.Args()[1:], serverJar)
		if err != nil {
			logger.Fatal("Invalid arguments", zap.Error(err))
		}

		cmd = exec.Command(flag.Arg(0), args...)
	} else {
		if args.Shell != "" {
			cmd = exec.Command(args.Shell, flag.Args()...)
		} else {
			cmd = exec.Command(flag.Arg(0), flag.Args()[1:]...)
		}
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		logger.Error("Unable to get stdin", zap.Error(err))
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		logger.Error("Unable to get stdout", zap.Error(err))
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		logger.Error("Unable to get stderr", zap.Error(err))
	}

	err = cmd.Start()
	if err != nil {
		logger.Error("Failed to start", zap.Error(err))
	}

	if args.Bootstrap != "" {
		bootstrapContent, err := ioutil.ReadFile(args.Bootstrap)
		if err != nil {
			logger.Error("Failed to read bootstrap commands", zap.Error(err))
		}
		_, err = stdin.Write(bootstrapContent)
		if err != nil {
			logger.Error("Failed to write bootstrap content", zap.Error(err))
		}
	}

	// Relay stdin/out/err between outside and server
	go func() {
		io.Copy(os.Stdout, stdout)
	}()
	go func() {
		io.Copy(os.Stderr, stderr)
	}()
	if !args.DetachStdin {
		go func() {
			io.Copy(stdin, os.Stdin)
		}()
	}

	cmdExitChan := make(chan int, 1)

	go func() {
		waitErr := cmd.Wait()
		if waitErr != nil {
			if exitErr, ok := waitErr.(*exec.ExitError); ok {
				exitCode := exitErr.ExitCode()
				logger.Warn("sub-process failed",
					zap.Int("exitCode", exitCode))
				cmdExitChan <- exitCode
			} else {
				logger.Error("command failed abnormally", zap.Error(waitErr))
				cmdExitChan <- 1
			}
			return
		} else {
			cmdExitChan <- 0
		}
	}()

	for {
		select {
		case <-signalChan:
			if hasRconCli() {
				err := stopWithRconCli()
				if err != nil {
					logger.Error("ERROR Failed to stop using rcon-cli", zap.Error(err))
					stopViaConsole(logger, stdin)
				}
			} else {
				stopViaConsole(logger, stdin)
			}

			logger.Info("Waiting for completion...")
			if args.StopDuration != 0 {
				time.AfterFunc(args.StopDuration, func() {
					logger.Error("ERROR Took too long, so killing server process")
					err := cmd.Process.Kill()
					if err != nil {
						logger.Error("ERROR failed to forcefully kill process")
					}
				})
			}

		case exitCode := <-cmdExitChan:
			logger.Info("Done")
			os.Exit(exitCode)
		}
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
	port := os.Getenv("RCON_PORT")
	if port == "" {
		port = "25575"
	}

	password := os.Getenv("RCON_PASSWORD")
	if password == "" {
		password = "minecraft"
	}

	log.Println("Stopping with rcon-cli")
	rconCliCmd := exec.Command("rcon-cli",
		"--port", port,
		"--password", password,
		"stop")

	return rconCliCmd.Run()
}

func announceStopViaConsole(logger *zap.Logger, stdin io.Writer, shutdownDelay time.Duration) {
	logger.Info("Sending shutdown announce 'say' to Minecraft server...")
	_, err := stdin.Write([]byte(fmt.Sprintf("say Server shutting down in %0.f seconds\n", shutdownDelay.Seconds())))
	if err != nil {
		logger.Error("ERROR failed to write say command to server console", zap.Error(err))
	}
}

func stopViaConsole(logger *zap.Logger, stdin io.Writer) {
	logger.Info("Sending 'stop' to Minecraft server...")
	_, err := stdin.Write([]byte("stop\n"))
	if err != nil {
		logger.Error("ERROR failed to write stop command to server console", zap.Error(err))
	}
}

func fillServerJar(args []string, serverJar string) ([]string, error) {
	result := make([]string, len(args))
	found := false
	for i, arg := range args {
		if arg == "_SERVERJAR_" {
			found = true
			result[i] = serverJar
		} else {
			result[i] = arg
		}
	}

	if !found {
		return nil, fmt.Errorf("unable to locate _SERVERJAR_ placeholder in args")
	} else {
		return result, nil
	}
}
