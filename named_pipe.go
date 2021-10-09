//go:build !linux

package main

import (
	"go.uber.org/zap"
	"io"
)

func handleNamedPipe(ctx context.Context, path string, stdin io.Writer) error {
	// does nothing on non-linux
	return nil
}
