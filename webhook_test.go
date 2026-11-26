package main

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type receivedWebhook struct {
	method      string
	contentType string
	// payload is decoded generically to be able to tell absent fields from zero values
	payload map[string]any
}

// newWebhookReceiver starts a server that provides the webhook requests it receives
func newWebhookReceiver(t *testing.T) (*httptest.Server, <-chan receivedWebhook) {
	t.Helper()
	received := make(chan receivedWebhook, 10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("failed to read webhook request: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("webhook request is not JSON: %v: %s", err, body)
		}
		received <- receivedWebhook{
			method:      r.Method,
			contentType: r.Header.Get("Content-Type"),
			payload:     payload,
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	return server, received
}

func nextWebhook(t *testing.T, received <-chan receivedWebhook) receivedWebhook {
	t.Helper()
	select {
	case webhook := <-received:
		return webhook
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for webhook request")
		return receivedWebhook{}
	}
}

func expectNoMoreWebhooks(t *testing.T, received <-chan receivedWebhook) {
	t.Helper()
	select {
	case webhook := <-received:
		t.Errorf("unexpected webhook request: %v", webhook.payload)
	case <-time.After(100 * time.Millisecond):
	}
}

func expectPayload(t *testing.T, webhook receivedWebhook, want map[string]any) {
	t.Helper()
	if webhook.method != http.MethodPost {
		t.Errorf("method = %s, want POST", webhook.method)
	}
	if webhook.contentType != "application/json" {
		t.Errorf("content type = %s, want application/json", webhook.contentType)
	}
	if !reflect.DeepEqual(webhook.payload, want) {
		t.Errorf("payload = %v, want %v", webhook.payload, want)
	}
}

// unusedAddress provides an address where nothing is listening
func unusedAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func TestLookupServerInfo(t *testing.T) {
	t.Setenv("WEBHOOK_TEST_TYPE", "PAPER")
	t.Setenv("WEBHOOK_TEST_MOTD", "A \"quoted\" server")
	t.Setenv("WEBHOOK_TEST_EMPTY", "")
	t.Setenv("WEBHOOK_TEST_NOT_LISTED", "secret")

	tests := []struct {
		name string
		vars []string
		want map[string]string
	}{
		{name: "none", vars: nil, want: map[string]string{}},
		{name: "blank entry", vars: []string{""}, want: map[string]string{}},
		{
			name: "only listed",
			vars: []string{"WEBHOOK_TEST_TYPE", "WEBHOOK_TEST_MOTD"},
			want: map[string]string{"WEBHOOK_TEST_TYPE": "PAPER", "WEBHOOK_TEST_MOTD": "A \"quoted\" server"},
		},
		{
			name: "surrounding spaces",
			vars: []string{" WEBHOOK_TEST_TYPE", "WEBHOOK_TEST_MOTD "},
			want: map[string]string{"WEBHOOK_TEST_TYPE": "PAPER", "WEBHOOK_TEST_MOTD": "A \"quoted\" server"},
		},
		{
			name: "set but empty is included",
			vars: []string{"WEBHOOK_TEST_EMPTY"},
			want: map[string]string{"WEBHOOK_TEST_EMPTY": ""},
		},
		{
			name: "unset is left out",
			vars: []string{"WEBHOOK_TEST_TYPE", "WEBHOOK_TEST_UNSET"},
			want: map[string]string{"WEBHOOK_TEST_TYPE": "PAPER"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lookupServerInfo(tt.vars)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("lookupServerInfo() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWebhookNotifierDisabledWithoutUrl(t *testing.T) {
	notifier := newWebhookNotifier(zap.NewNop(), "", []string{"TYPE"}, time.Second)
	if notifier != nil {
		t.Fatalf("newWebhookNotifier() = %v, want nil", notifier)
	}
	// should be usable without checking for nil
	notifier.Starting()
	notifier.Exited(0)
	notifier.Exited(1)
}

func TestWebhookNotifierEvents(t *testing.T) {
	t.Setenv("WEBHOOK_TEST_TYPE", "PAPER")
	t.Setenv("WEBHOOK_TEST_VERSION", "1.21.10")
	t.Setenv("WEBHOOK_TEST_NOT_LISTED", "secret")
	serverInfoVars := []string{"WEBHOOK_TEST_TYPE", "WEBHOOK_TEST_VERSION"}
	serverInfo := map[string]any{"WEBHOOK_TEST_TYPE": "PAPER", "WEBHOOK_TEST_VERSION": "1.21.10"}

	tests := []struct {
		name     string
		exitCode int
		want     map[string]any
	}{
		{
			name:     "stopped",
			exitCode: 0,
			want:     map[string]any{"event": "stopped", "serverInfo": serverInfo},
		},
		{
			name:     "failed",
			exitCode: 3,
			// numbers are decoded as float64
			want: map[string]any{"event": "failed", "exitStatus": float64(3), "serverInfo": serverInfo},
		},
		{
			name:     "killed",
			exitCode: -1,
			want:     map[string]any{"event": "failed", "exitStatus": float64(-1), "serverInfo": serverInfo},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, received := newWebhookReceiver(t)
			notifier := newWebhookNotifier(zap.NewNop(), server.URL, serverInfoVars, 5*time.Second)

			notifier.Starting()
			expectPayload(t, nextWebhook(t, received),
				map[string]any{"event": "starting", "serverInfo": serverInfo})

			notifier.Exited(tt.exitCode)
			expectPayload(t, nextWebhook(t, received), tt.want)

			expectNoMoreWebhooks(t, received)
		})
	}
}

func TestWebhookNotifierWithoutServerInfo(t *testing.T) {
	server, received := newWebhookReceiver(t)
	notifier := newWebhookNotifier(zap.NewNop(), server.URL, nil, 5*time.Second)

	notifier.Exited(0)
	expectPayload(t, nextWebhook(t, received),
		map[string]any{"event": "stopped", "serverInfo": map[string]any{}})
}

func TestWebhookNotifierKeepsEventsInOrder(t *testing.T) {
	var mutex sync.Mutex
	var events []string
	first := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutex.Lock()
		delay := first
		first = false
		mutex.Unlock()
		if delay {
			time.Sleep(200 * time.Millisecond)
		}

		var payload webhookPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("webhook request is not JSON: %v", err)
		}
		mutex.Lock()
		events = append(events, payload.Event)
		mutex.Unlock()
	}))
	defer server.Close()

	notifier := newWebhookNotifier(zap.NewNop(), server.URL, nil, 5*time.Second)
	notifier.Starting()
	// ...and the server process exits right away
	notifier.Exited(0)

	mutex.Lock()
	defer mutex.Unlock()
	want := []string{"starting", "stopped"}
	if !reflect.DeepEqual(events, want) {
		t.Errorf("events = %v, want %v", events, want)
	}
}

func TestWebhookNotifierSlowEndpointIsBoundedByTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// never responds within the test
		<-release
	}))
	defer server.Close()
	defer close(release)

	const timeout = 300 * time.Millisecond
	// allows for the starting event still being in flight plus some scheduling slack
	const limit = timeout + 2*time.Second

	tests := []struct {
		name         string
		withStarting bool
	}{
		{name: "exit only", withStarting: false},
		{name: "starting still pending", withStarting: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			core, logs := observer.New(zap.WarnLevel)
			notifier := newWebhookNotifier(zap.New(core), server.URL, nil, timeout)

			start := time.Now()
			if tt.withStarting {
				notifier.Starting()
			}
			notifier.Exited(1)
			elapsed := time.Since(start)

			if elapsed < timeout {
				t.Errorf("returned after %s, before the timeout of %s", elapsed, timeout)
			}
			if elapsed > limit {
				t.Errorf("returned after %s, want at most %s", elapsed, limit)
			}
			if logs.FilterMessage("Failed to send webhook event").Len() == 0 {
				t.Errorf("expected the failed delivery to be logged")
			}
		})
	}
}

func TestWebhookNotifierUnreachableEndpoint(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	webhookURL := "http://" + unusedAddress(t) + "/hooks/secret-token"
	notifier := newWebhookNotifier(zap.New(core), webhookURL, nil, 5*time.Second)

	start := time.Now()
	notifier.Starting()
	notifier.Exited(0)
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("returned after %s, want it to fail fast", elapsed)
	}

	entries := logs.FilterMessage("Failed to send webhook event").All()
	if len(entries) != 2 {
		t.Fatalf("got %d logged failures, want 2", len(entries))
	}
	for _, entry := range entries {
		for _, field := range entry.Context {
			if err, ok := field.Interface.(error); ok && strings.Contains(err.Error(), "secret-token") {
				t.Errorf("logged error contains the webhook URL: %v", err)
			}
		}
	}
}

func TestWebhookNotifierErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer server.Close()

	core, logs := observer.New(zap.WarnLevel)
	notifier := newWebhookNotifier(zap.New(core), server.URL, nil, 5*time.Second)

	notifier.Exited(0)

	if logs.FilterMessage("Failed to send webhook event").Len() != 1 {
		t.Errorf("expected the error response to be logged")
	}
}

// The following tests run the runner with the scripts in test/. The test binary is the runner
// when BE_MC_SERVER_RUNNER is set.

func TestMain(m *testing.M) {
	if os.Getenv("BE_MC_SERVER_RUNNER") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runnerCommand(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is needed for the scripts in test/")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(),
		"BE_MC_SERVER_RUNNER=1",
		// stop via the console of the test scripts
		"ENABLE_RCON=false",
		"TYPE=PAPER",
		"VERSION=1.21.10",
		"RCON_PASSWORD=secret",
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd
}

func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	t.Fatalf("failed to run: %v", err)
	return 0
}

func TestRunnerSendsWebhookEvents(t *testing.T) {
	serverInfo := map[string]any{"TYPE": "PAPER", "VERSION": "1.21.10"}

	tests := []struct {
		name string
		args []string
		// sigterm is sent after the starting event was received
		sigterm      bool
		wantExitCode int
		want         map[string]any
	}{
		{
			name:         "exits successfully",
			args:         []string{"test/exit-with-code.sh", "0"},
			wantExitCode: 0,
			want:         map[string]any{"event": "stopped", "serverInfo": serverInfo},
		},
		{
			name:         "exits with failure",
			args:         []string{"test/exit-with-code.sh", "3"},
			wantExitCode: 3,
			want:         map[string]any{"event": "failed", "exitStatus": float64(3), "serverInfo": serverInfo},
		},
		{
			name:         "gracefully stopped by signal",
			args:         []string{"test/stop-on-command.sh"},
			sigterm:      true,
			wantExitCode: 0,
			want:         map[string]any{"event": "stopped", "serverInfo": serverInfo},
		},
		{
			name:    "killed after stop duration",
			args:    []string{"-stop-duration", "300ms", "test/echo.sh"},
			sigterm: true,
			// os.Exit(-1)
			wantExitCode: 255,
			want:         map[string]any{"event": "failed", "exitStatus": float64(-1), "serverInfo": serverInfo},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, received := newWebhookReceiver(t)

			args := append([]string{
				"-webhook-url", server.URL,
				"-webhook-server-info", "TYPE,VERSION,WEBHOOK_TEST_UNSET",
			}, tt.args...)
			cmd := runnerCommand(t, args...)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			exited := make(chan error, 1)
			go func() {
				exited <- cmd.Wait()
			}()

			expectPayload(t, nextWebhook(t, received),
				map[string]any{"event": "starting", "serverInfo": serverInfo})

			if tt.sigterm {
				if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			}

			expectPayload(t, nextWebhook(t, received), tt.want)

			select {
			case err := <-exited:
				if exitCode := exitCodeOf(t, err); exitCode != tt.wantExitCode {
					t.Errorf("exit code = %d, want %d", exitCode, tt.wantExitCode)
				}
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				t.Fatal("timed out waiting for runner to exit")
			}

			expectNoMoreWebhooks(t, received)
		})
	}
}

func TestRunnerExitNotDelayedByUnreachableWebhook(t *testing.T) {
	cmd := runnerCommand(t,
		"-webhook-url", "http://"+unusedAddress(t)+"/",
		"test/exit-with-code.sh", "3")

	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)

	if exitCode := exitCodeOf(t, err); exitCode != 3 {
		t.Errorf("exit code = %d, want 3", exitCode)
	}
	if elapsed > webhookTimeout {
		t.Errorf("runner took %s to exit, want less than the webhook timeout of %s", elapsed, webhookTimeout)
	}
}

func TestRunnerExitBoundedWithUnresponsiveWebhook(t *testing.T) {
	if testing.Short() {
		t.Skip("takes as long as the webhook timeout")
	}

	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// never responds within the test
		<-release
	}))
	defer server.Close()
	defer close(release)

	cmd := runnerCommand(t, "-webhook-url", server.URL, "test/exit-with-code.sh", "3")

	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)

	if exitCode := exitCodeOf(t, err); exitCode != 3 {
		t.Errorf("exit code = %d, want 3", exitCode)
	}
	// with some slack for starting the processes
	if limit := webhookTimeout + 3*time.Second; elapsed > limit {
		t.Errorf("runner took %s to exit, want at most %s", elapsed, limit)
	}
}
