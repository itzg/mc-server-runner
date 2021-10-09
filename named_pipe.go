//go:build !linux
// +build !linux

package main

import (
	"context"
	"io"
)

func handleNamedPipe(ctx context.Context, path string, stdin io.Writer, errors chan error) error {
	// does nothing on non-linux
	return nil
}
