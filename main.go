package main

import (
	"flag"
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

func main() {
	signalChan := make(chan os.Signal, 1)
	// docker stop sends a SIGTERM, so intercept that and send a 'stop' command to the server
	signal.Notify(signalChan, syscall.SIGTERM)

	bootstrap := flag.String("bootstrap", "", "Specifies a file with commands to initially send to the server")
	stopDuration := flag.String("stop-duration", "", "Amount of time in Golang duration to wait after sending the 'stop' command.")
	detachStdin := flag.Bool("detach-stdin", false, "Don't forward stdin and allow process to be put in background")
	shell := flag.String("shell", "bash", "The shell to use for launching scripts")
	flag.Parse()

	if flag.NArg() < 1 {
		log.Fatal("Missing executable arguments")
	}

	var cmd *exec.Cmd
	if strings.HasSuffix(flag.Arg(0), ".sh") {
		cmd = exec.Command(*shell, flag.Args()...)
	} else {
		if flag.NArg() > 1 {
			cmd = exec.Command(flag.Arg(0), flag.Args()[1:]...)
		} else {
			cmd = exec.Command(flag.Arg(0))
		}
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		log.Fatalf("Unable to get stdin: %s", err.Error())
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Fatalf("Unable to get stdout: %s", err.Error())
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		log.Fatalf("Unable to get stderr: %s", err.Error())
	}

	err = cmd.Start()
	if err != nil {
		log.Fatalf("Failed to start: %s", err.Error())
	}

	if *bootstrap != "" {
		bootstrapContent, err := ioutil.ReadFile(*bootstrap)
		if err != nil {
			log.Fatalf("Failed to read bootstrap commands: %s", err.Error())
		}
		_, err = stdin.Write(bootstrapContent)
		if err != nil {
			log.Fatalf("Failed to write bootstrap content: %s", err.Error())
		}
	}

	// Relay stdin/out/err between outside and server
	go func() {
		io.Copy(os.Stdout, stdout)
	}()
	go func() {
		io.Copy(os.Stderr, stderr)
	}()
	if !*detachStdin {
		go func() {
			io.Copy(stdin, os.Stdin)
		}()
	}

	procDone := make(chan struct{}, 1)
	go func() {
		cmd.Wait()
		procDone <- struct{}{}
	}()

	for {
		select {
		case <-signalChan:
			if hasRconCli() {
				err := stopWithRconCli()
				if err != nil {
					log.Println("ERROR Failed to stop using rcon-cli", err)
					stopViaConsole(stdin)
				}
			} else {
				stopViaConsole(stdin)
			}

			log.Print("Waiting for completion...")
			if *stopDuration != "" {
				if d, err := time.ParseDuration(*stopDuration); err == nil {
					time.AfterFunc(d, func() {
						log.Print("ERROR Took too long, so killing server process")
						err := cmd.Process.Kill()
						if err != nil {
							log.Println("ERROR failed to forcefully kill process")
						}
					})
				} else {
					log.Printf("ERROR Invalid stop duration: '%v'", *stopDuration)
				}
			}

		case <-procDone:
			log.Print("Done")
			return
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

func stopViaConsole(stdin io.Writer) {
	log.Print("Sending 'stop' to Minecraft server...")
	_, err := stdin.Write([]byte("stop\n"))
	if err != nil {
		log.Println("ERROR failed to write stop command to server console")
	}
}
