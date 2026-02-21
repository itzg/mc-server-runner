//go:build !linux

package main

import "go.uber.org/zap"

func logSystemMemory(logger *zap.Logger) {
	logger.Info("System memory info is only available on Linux")
}
