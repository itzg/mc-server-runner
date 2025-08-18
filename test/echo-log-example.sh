#!/bin/bash

# This script tests the console forwarding functionality and can trigger issues with very long lines.
# The bug was originally discovered in the SSH server.

echo "Here is a problematic log file..."
cat $(dirname $0)/log-example.txt
