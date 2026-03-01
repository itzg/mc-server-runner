package main

import (
	"github.com/pbnjay/memory"
	"go.uber.org/zap"
)

func logSystemMemory(logger *zap.Logger) {
	totalMB := memory.TotalMemory() / 1024 / 1024
	freeMB := memory.FreeMemory() / 1024 / 1024
	usedMB := totalMB - freeMB

	logger.Info("System memory at time of exit",
		zap.Uint64("totalMB", totalMB),
		zap.Uint64("freeMB", freeMB),
		zap.Uint64("usedMB", usedMB),
		zap.Float64("usagePercent", float64(usedMB)*100/float64(totalMB)),
	)
}