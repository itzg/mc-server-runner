package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"syscall"
)

func handleNamedPipe(ctx context.Context, path string, stdin io.Writer) error {
	fi, statErr := os.Stat(path)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			mkErr := syscall.Mkfifo(path, 0666)
			if mkErr != nil {
				return fmt.Errorf("failed to create named pipe: %w", mkErr)
			}
		} else {
			return fmt.Errorf("failed to stat named pipe: %w", statErr)
		}
	} else {
		// already exists...named pipe?
		if fi.Mode().Type()&os.ModeNamedPipe == 0 {
			return fmt.Errorf("existing path '%s' is not a named pipe", path)
		}
	}

	f, openErr := os.Open(path)
	if openErr != nil {
		return fmt.Errorf("failed to open named pipe: %w", openErr)
	}

	go func() {
		_, _ = io.Copy(stdin, f)
		// copy finished due to file closure (or error)
		os.Remove(path)
	}()

	go func() {
		<-ctx.Done()
		// kick the copy operation above
		f.Close()
	}()

	return nil
}
