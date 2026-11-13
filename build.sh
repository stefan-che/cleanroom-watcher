#!/usr/bin/env bash
# Builds the Linux binary inside Docker and places scanner in the current directory
set -e

docker run --rm \
  -v "$(pwd)":/src \
  -w /src \
  golang:1.25-alpine \
  go build -ldflags="-w -s" -o scanner ./cmd/scanner

echo "Done: $(ls -lh scanner | awk '{print $5, $9}')"
