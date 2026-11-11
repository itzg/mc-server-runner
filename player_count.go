package main

import (
	"errors"
	"fmt"
	"math"
	"time"

	mcpinger "github.com/Raqbit/mc-pinger"
	"go.uber.org/zap"
)

// playerCountQueryTimeout is kept short since the query happens while the server is waiting to be stopped
const playerCountQueryTimeout = 2 * time.Second

// playerCountFunc provides the number of players that are currently online
type playerCountFunc func() (int, error)

// queryOnlinePlayers retrieves the number of online players from the Minecraft Java server at the
// given host and port using the server list ping protocol, which doesn't require RCON or the console.
func queryOnlinePlayers(host string, port int, timeout time.Duration) (int, error) {
	if port < 1 || port > math.MaxUint16 {
		return 0, fmt.Errorf("invalid server port: %d", port)
	}

	info, err := mcpinger.New(host, uint16(port), mcpinger.WithTimeout(timeout)).Ping()
	if err != nil {
		return 0, err
	}

	// A server that is still starting up can answer pings with an empty status, so a missing max
	// player count is used to tell that apart from a server that actually has no players online.
	if info.Players.Max == 0 {
		return 0, errors.New("server status is missing player info")
	}

	return int(info.Players.Online), nil
}

// shouldAnnounceStop decides if the server stop needs to be announced and delayed by announceDelay.
// With skipWhenEmpty the delay is only bypassed when onlinePlayers positively reports that
// nobody is online. If that can't be determined, the delay is retained to be on the safe side.
func shouldAnnounceStop(logger *zap.Logger, announceDelay time.Duration, skipWhenEmpty bool, onlinePlayers playerCountFunc) bool {
	if announceDelay <= 0 {
		return false
	}
	if !skipWhenEmpty {
		return true
	}

	online, err := onlinePlayers()
	if err != nil {
		logger.Warn("Unable to query online player count, retaining StopServerAnnounceDelay", zap.Error(err))
		return true
	}
	if online > 0 {
		logger.Debug("Players are online, retaining StopServerAnnounceDelay", zap.Int("online", online))
		return true
	}

	logger.Info("No players online, bypassing StopServerAnnounceDelay")
	return false
}
