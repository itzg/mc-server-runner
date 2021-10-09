package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"syscall"
)

func handleNamedPipe(ctx context.Context, path string, stdin io.Writer, errors chan error) error {
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

	go func() {
		//goland:noinspection GoUnhandledErrorResult
		defer os.Remove(path)

		for {
			select {
			case <-ctx.Done():
				return

			default:
				f, openErr := os.Open(path)
				if openErr != nil {
					errors <- fmt.Errorf("failed to open named fifo: %w", openErr)
					return
				}

				_, copyErr := io.Copy(stdin, f)
				if copyErr != nil {
					errors <- fmt.Errorf("unexpected error reading named pipe: %w", copyErr)
				}
				f.Close()
			}
		}
	}()

	return nil
}
