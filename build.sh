#!/bin/bash

# Build script for go-ordfs-server
# This script compiles the Go server and outputs it as server.run in the root directory

set -e  # Exit on any error

echo "Building go-ordfs-server..."

# Build the server binary
go build -o server.run ./cmd/server

echo "Build completed successfully. Binary is available as server.run"
