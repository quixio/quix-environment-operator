#!/bin/bash

set -e

# Build the test Docker image
echo "Building test Docker image..."
docker build -t quix-environment-operator-test -f Dockerfile.test .

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
    "go test $tag_options ./controllers/... -v"
}

# Run all or specific test types based on command line args
if [ $# -eq 0 ] || [ "$1" == "all" ]; then
  # Run unit tests first
  run_tests "Unit and Controller" ""
  
  # Then run integration tests
  run_tests "Integration" "-tags=integration"
  
  echo ""
  echo "✅ All tests completed successfully"
elif [ "$1" == "unit" ]; then
  # Run only unit tests (tagged with 'short')
  run_tests "Unit" "-short"
elif [ "$1" == "controller" ]; then
  # Run controller tests
  run_tests "Controller" "./controllers/environment_controller_test.go"
elif [ "$1" == "integration" ]; then
  # Run only integration tests
  run_tests "Integration" "-tags=integration"
else
  echo "Unknown test type: $1"
  echo "Usage: $0 [all|unit|controller|integration]"
  exit 1
fi 