package main

import (
	"golang.org/x/sys/unix"

	"go.uber.org/zap"
)

func logSystemMemory(logger *zap.Logger) {
	var info unix.Sysinfo_t
	if err := unix.Sysinfo(&info); err != nil {
		logger.Warn("Unable to retrieve system memory info", zap.Error(err))
		return
	}

	unit := uint64(info.Unit)
	totalMB := info.Totalram * unit / 1024 / 1024
	freeMB := info.Freeram * unit / 1024 / 1024
	availBufferCacheMB := (info.Freeram + info.Bufferram) * unit / 1024 / 1024

	logger.Info("System memory at time of exit",
		zap.Uint64("totalMB", totalMB),
		zap.Uint64("freeMB", freeMB),
		zap.Uint64("availableWithBuffersMB", availBufferCacheMB),
	)
}
