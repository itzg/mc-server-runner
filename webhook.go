package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"
)

// webhookTimeout bounds the delivery of each webhook event, so that an unreachable or slow
// endpoint can't hold up the exit of the runner for longer than this.
const webhookTimeout = 5 * time.Second

const (
	webhookEventStarting = "starting"
	webhookEventStopped  = "stopped"
	webhookEventFailed   = "failed"
)

type webhookPayload struct {
	// Event is one of starting, stopped, or failed
	Event string `json:"event"`
	// ExitStatus is the exit status of the server process and only present when Event is failed
	ExitStatus *int `json:"exitStatus,omitempty"`
	// ServerInfo contains the requested environment variables and their values
	ServerInfo map[string]string `json:"serverInfo"`
}

// webhookNotifier posts the lifecycle events of the server process to a webhook URL.
// A nil notifier is valid and does nothing, which is the case when no URL is configured.
type webhookNotifier struct {
	logger     *zap.Logger
	url        string
	serverInfo map[string]string
	timeout    time.Duration
	client     *http.Client
	// startingDone is closed when the delivery of the starting event has finished
	startingDone chan struct{}
}

// newWebhookNotifier returns nil when webhookURL is empty, since that disables webhook support.
func newWebhookNotifier(logger *zap.Logger, webhookURL string, serverInfoVars []string, timeout time.Duration) *webhookNotifier {
	if webhookURL == "" {
		return nil
	}
	return &webhookNotifier{
		logger:     logger,
		url:        webhookURL,
		serverInfo: lookupServerInfo(serverInfoVars),
		timeout:    timeout,
		client:     &http.Client{},
	}
}

// lookupServerInfo resolves the given environment variable names into a map of their values.
// Variables that are not set are left out.
func lookupServerInfo(vars []string) map[string]string {
	info := make(map[string]string)
	for _, name := range vars {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if value, ok := os.LookupEnv(name); ok {
			info[name] = value
		}
	}
	return info
}

// Starting sends the starting event in the background, so that it doesn't delay the server.
func (n *webhookNotifier) Starting() {
	if n == nil {
		return
	}
	n.startingDone = make(chan struct{})
	go func() {
		defer close(n.startingDone)
		ctx, cancel := context.WithTimeout(context.Background(), n.timeout)
		defer cancel()
		n.send(ctx, webhookPayload{Event: webhookEventStarting, ServerInfo: n.serverInfo})
	}()
}

// Exited sends the stopped event when exitCode is zero and otherwise the failed event.
// It returns when the event was delivered, delivery failed, or the timeout elapsed.
func (n *webhookNotifier) Exited(exitCode int) {
	if n == nil {
		return
	}
	payload := webhookPayload{Event: webhookEventStopped, ServerInfo: n.serverInfo}
	if exitCode != 0 {
		payload.Event = webhookEventFailed
		payload.ExitStatus = &exitCode
	}

	ctx, cancel := context.WithTimeout(context.Background(), n.timeout)
	defer cancel()

	if n.startingDone != nil {
		// keep the events in order when the server exits before the starting event got delivered
		select {
		case <-n.startingDone:
		case <-ctx.Done():
			n.logger.Warn("Failed to send webhook event",
				zap.String("event", payload.Event), zap.Error(ctx.Err()))
			return
		}
	}
	n.send(ctx, payload)
}

func (n *webhookNotifier) send(ctx context.Context, payload webhookPayload) {
	err := n.post(ctx, payload)
	if err != nil {
		n.logger.Warn("Failed to send webhook event",
			zap.String("event", payload.Event), zap.Error(err))
	} else {
		n.logger.Debug("Sent webhook event", zap.String("event", payload.Event))
	}
}

func (n *webhookNotifier) post(ctx context.Context, payload webhookPayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, bytes.NewReader(body))
	if err != nil {
		return withoutURL(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "mc-server-runner")

	resp, err := n.client.Do(req)
	if err != nil {
		return withoutURL(err)
	}
	//goland:noinspection GoUnhandledErrorResult
	defer resp.Body.Close()
	// allows for connection re-use, but don't let a large or endless response hold things up
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("unexpected response status: %s", resp.Status)
	}
	return nil
}

// withoutURL unwraps the given error when it is a url.Error, since those include the URL in
// the message and webhook URLs tend to contain a secret.
func withoutURL(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}
