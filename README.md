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
  -shell string
        When set, pass the arguments to this shell
  -stop-duration duration
        Amount of time in Golang duration to wait after sending the 'stop' command.
  -stop-server-announce-delay duration
        Amount of time in Golang duration to wait after announcing server shutdown
```

## Development Testing

Start a golang container for building and execution:

```bash
docker run -it --rm \
  -v ${PWD}:/build \
  -w /build \
  circleci/golang:1.12
```

Within that container, build/test by running:

```bash
go run main.go test/dump.sh
go run main.go test/bash-only.sh
# The following should fail
go run main.go --shell sh test/bash-only.sh
```