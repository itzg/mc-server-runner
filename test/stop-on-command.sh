#!/bin/bash

# This script exits successfully once "stop" is received on stdin, like a Minecraft server would

echo "Waiting for stop command..."
while read -r line; do
  if [[ $line == stop ]]; then
    echo "Stopping"
    exit 0
  fi
done
exit 1
