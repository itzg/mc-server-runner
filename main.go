package main

import (
	"strings"
	"os"
	"os/exec"
	"os/signal"
	"log"
	"io"
	"time"
	"flag"
	"io/ioutil"
	"syscall"
)

func main() {
	signalChan := make(chan os.Signal, 1)
	// docker stop sends a SIGTERM, so intercept that and send a 'stop' command to the server
	signal.Notify(signalChan, syscall.SIGTERM)

	bootstrap := flag.String("bootstrap", "", "Specifies a file with commands to initially send to the server")
	stopDuration := flag.String("stop-duration", "", "Amount of time in Golang duration to wait after sending the 'stop' command.")
	detachStdin := flag.Bool("detach-stdin", false, "Don't forward stdin and allow process to be put in background")
	flag.Parse()

	if flag.NArg() < 1 {
		log.Fatal("Missing executable arguments")
	}

	var cmd *exec.Cmd
	if strings.HasSuffix(flag.Arg(0), ".sh") {
		cmd = exec.Command("sh", flag.Args()...)
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

	cmd.Start()

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
			log.Print("Sending 'stop' to Minecraft server...")
			stdin.Write([]byte("stop\n"))

			log.Print("Waiting for completion...")
			if *stopDuration != "" {
				if d, err := time.ParseDuration(*stopDuration); err == nil {
					time.AfterFunc(d, func() {
						log.Print("Took too long, so killing server process")
						cmd.Process.Kill()
					})
				} else {
					log.Printf("Invalid stop duration: '%v'", *stopDuration)
				}
			}

		case <-procDone:
			log.Print("Done")
			return
		}
	}

}
