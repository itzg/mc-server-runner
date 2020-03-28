This is a process wrapper used by 
[the itzg/minecraft-server Docker image](https://hub.docker.com/r/itzg/minecraft-server/)
to ensure the Minecraft server is stopped gracefully when the container is sent the `TERM` signal.

## Usage

> Available at any time using `-h`

```
  -bootstrap string
    	Specifies a file with commands to initially send to the server
  -detach-stdin
    	Don't forward stdin and allow process to be put in background
  -shell string
    	The shell to use for launching scripts
  -stop-duration string
    	Amount of time in Golang duration to wait after sending the 'stop' command.
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