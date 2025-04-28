#!/bin/bash

set -e

# Generate go.sum if it doesn't exist
echo "Checking if go.sum exists and generating if needed..."
if [ ! -f "go.sum" ]; then
  echo "go.sum doesn't exist, generating..."
  go mod tidy
fi
# Build the test Docker image
echo "Building test Docker image..."
docker build -t quix-environment-operator-test -f build/dockerfile.test . --progress=plain

echo ""
echo "=== Running tests ==="
echo ""

mkdir -p $(pwd)/coverlet
docker run --rm \
  -v "$(pwd)/coverlet":/app/coverlet \
  quix-environment-operator-test

echo ""
echo "✅ All tests completed successfully"