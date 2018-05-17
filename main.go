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
	signal.Notify(signalChan, syscall.SIGTERM)

	bootstrap := flag.String("bootstrap", "", "Specifies commands to initially send to the server")
	flag.Parse()

	if flag.NArg() < 1 {
		log.Fatal("Missing executable arguments")
	}

	var cmd *exec.Cmd
	if strings.HasSuffix(flag.Arg(0), ".sh") {
		cmd = exec.Command("sh", os.Args...)
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

	go func() {
		io.Copy(os.Stdout, stdout)
	}()
	go func() {
		io.Copy(os.Stderr, stderr)
	}()

	<-signalChan
	log.Print("Sending 'stop' to Minecraft server...")
	stdin.Write([]byte("stop\n"))

	log.Print("Waiting for completion...")
	time.AfterFunc(5*time.Second, func() {
		log.Print("Took too long, so killing server process")
		cmd.Process.Kill()
	})
	cmd.Wait()
	log.Print("Done")
}
