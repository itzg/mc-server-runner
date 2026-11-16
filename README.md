[![GitHub release (latest SemVer)](https://img.shields.io/github/v/release/itzg/mc-server-runner)](https://github.com/itzg/mc-server-runner/releases/latest)
[![Test](https://github.com/itzg/mc-server-runner/actions/workflows/test.yml/badge.svg)](https://github.com/itzg/mc-server-runner/actions/workflows/test.yml)


This is a process wrapper used by 
[the itzg/minecraft-server Docker image](https://hub.docker.com/r/itzg/minecraft-server/)
to ensure the Minecraft server is stopped gracefully when the container is sent the `TERM` signal.

## Usage

> Available at any time using `-h`

```
  -bootstrap string
        Specifies a file with commands to initially send to the server
  -debug
        Enable debug logging
  -detach-stdin
        Don't forward stdin and allow process to be put in background
  -named-pipe string
        Optional path to create and read a named pipe for console input
  -remote-console
        Allow remote shell connections over SSH to server console
  -server-host string
        Host of the Minecraft server to query for its status (default "localhost")
  -server-port int
        Port of the Minecraft server to query for its status (default 25565)
  -shell string
        When set, pass the arguments to this shell
  -stop-command string
        Which command to send to the server to stop it (default "stop")
  -stop-duration duration
        Amount of time in Golang duration to wait after sending the 'stop' command.
  -stop-server-announce-delay duration
        Amount of time in Golang duration to wait after announcing server shutdown
  -stop-server-skip-delay-when-empty
        Bypass StopServerAnnounceDelay when the server status reports that no players are online
  -webhook-server-info value
        Comma-separated list of environment variable names to include with their values as server info in webhook events
  -webhook-url string
        When set, the server starting, stopped, and failed events are sent to this URL as a JSON POST
  -websocket-address string
        Bind address for websocket server (env WEBSOCKET_ADDRESS) (default "0.0.0.0:80")
  -websocket-allowed-origins value
        Comma-separated list of trusted origins (env WEBSOCKET_ALLOWED_ORIGINS)
  -websocket-console
        Allow remote shell over websocket
  -websocket-disable-authentication
        Disable websocket authentication (env WEBSOCKET_DISABLE_AUTHENTICATION)
  -websocket-disable-origin-check
        Disable checking if origin is trusted (env WEBSOCKET_DISABLE_ORIGIN_CHECK)
  -websocket-log-buffer-size int
        Number of log lines to save and send to connecting clients (env WEBSOCKET_LOG_BUFFER_SIZE) (default 50)
  -websocket-password string
        Password will be the same as RCON_PASSWORD if unset (env WEBSOCKET_PASSWORD)
```

The `-stop-server-announce-delay` can by bypassed by sending a `SIGUSR1` signal to the `mc-server-runner` process.  
This works in cases where a prior `SIGTERM` has already been sent **and** in cases where no prior signal has been sent.

With `-stop-server-skip-delay-when-empty` the announcement and delay are also bypassed when nobody is online at the time the `TERM` signal is received.
The player count is obtained by querying the status of the Java edition server at `-server-host` and `-server-port`, the same way a Minecraft client does for its server list.
If that query fails, such as when the server is still starting up, the announcement and delay proceed as usual.

## Webhook

When `-webhook-url` is set, a JSON payload is sent to that URL with a `POST` request at the following events:

| `event`    | Sent when                                                                                |
|------------|------------------------------------------------------------------------------------------|
| `starting` | The server process was started. It does not indicate the server is ready for connections |
| `stopped`  | The server process exited with a zero exit status                                        |
| `failed`   | The server process exited with a non-zero exit status, which is provided in `exitStatus` |

Only the exit status decides between `stopped` and `failed`, whether or not the stop was requested with a signal.
A server that is stopped gracefully after a `TERM` signal exits with zero and is reported as `stopped`.
One that gets killed after exceeding `-stop-duration` is reported as `failed` with an `exitStatus` of -1.

`serverInfo` contains the environment variables named by `-webhook-server-info` with their values as strings.
No other environment variables are included and ones that are not set are left out.

For example, with `-webhook-url http://example.com/hook -webhook-server-info TYPE,VERSION,MOTD` a failed server results in

```json
{
  "event": "failed",
  "exitStatus": 1,
  "serverInfo": {
    "TYPE": "PAPER",
    "VERSION": "1.21.10",
    "MOTD": "A Minecraft Server"
  }
}
```

`exitStatus` is only present with the `failed` event.

Delivery is attempted once per event with a timeout of 5 seconds. A failure, including a non-2xx response, is logged without the URL and otherwise ignored.
The `starting` event is sent in the background. The `stopped` or `failed` event is sent before the runner exits, so an unresponsive endpoint can delay the exit by up to that timeout.

## Development Testing

Start a golang container for building and execution:
> Port 2222 is used for remote ssh console  
Port 80 is used for remote websocket console
```bash
docker run -it --rm \
  -v ${PWD}:/build \
  -w /build \
  -p 2222:2222 \
  -p 80:80 \
  golang:1.19
```

Within that container, build/test by running:

```bash
go run . test/dump.sh
go run . test/bash-only.sh
# Used to test remote console functionality
# Connect to this using an ssh client from outside the container to ensure two-way communication works
go run . -remote-console /usr/bin/sh
# The following should fail
go run . --shell sh test/bash-only.sh
```

### Using the devcontainer's Dockerfile

#### With IntelliJ

Create a "Go Build" run configuration

![](notes/dockerfile-run-config.png)

with a Dockerfile target

![](notes/dockerfile-docker-target.png)
