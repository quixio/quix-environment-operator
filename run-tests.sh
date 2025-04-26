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
docker build -t quix-environment-operator-test -f dockerfile.test .

# Function to run tests with specified tags and options
run_tests() {
  local test_type=$1
  local tag_options=$2
  
  echo ""
  echo "=== Running $test_type tests ==="
  echo ""
  
  docker run --rm \
    -v "$(pwd)":/app \
    quix-environment-operator-test \
    go test $tag_options ./... -v
}

# Run all or specific test types based on command line args
if [ $# -eq 0 ] || [ "$1" == "all" ]; then
  # Then run integration tests
  run_tests "Integration" "-tags=integration"
  
  echo ""
  echo "✅ All tests completed successfully"
elif [ "$1" == "unit" ]; then
  # Run only unit tests (tagged with 'short')
  run_tests "Unit" "-short"
elif [ "$1" == "integration" ]; then
  # Run only integration tests
  run_tests "Integration" "-tags=integration"
else
  echo "Unknown test type: $1"
  echo "Usage: $0 [all|unit|integration]"
  exit 1
fi 