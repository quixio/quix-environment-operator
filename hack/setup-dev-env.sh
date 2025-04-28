#!/bin/bash

# This script sets up the development environment for the Quix Environment Operator.
# It installs required tools and configures environment variables.

set -e

# Tool Versions
CONTROLLER_TOOLS_VERSION=v0.14.0
GOIMPORTS_TOOLS_VERSION=v0.32.0
ENVTEST_K8S_VERSION=1.28.0
LOCALBIN="$(pwd)/bin"

export ARCH=$(uname -m) && \
    case "$ARCH" in \
        x86_64) export ARCH=amd64 ;; \
        aarch64) export ARCH=arm64 ;; \
        arm64) export ARCH=arm64 ;; \
        *) echo "Unsupported architecture: $ARCH" && exit 1 ;; \
    esac

export GOARCH=$ARCH

# test for go and install if not present
if ! command -v go &> /dev/null; then
    echo "Installing Go..."
    if [[ "$OSTYPE" == "darwin"* ]]; then
        echo "Installing Go for macOS..."
        brew install go
    fi
    if [[ "$OSTYPE" == "linux"* ]]; then
        echo "Installing Go for Linux..."
        sudo apt-get update
        sudo apt-get install -y golang
    fi
fi

# Add GOPATH/bin to PATH if not already there to ensure setup-envtest is available
if [[ ":$PATH:" != *":$HOME/go/bin:"* ]]; then
    export PATH="$HOME/go/bin:$PATH"
    echo "Added $HOME/go/bin to PATH"
fi
echo $LOCALBIN
test -s $LOCALBIN/goimports || GOARCH=$(go env GOARCH) GOBIN=$LOCALBIN go install golang.org/x/tools/cmd/goimports@$GOIMPORTS_TOOLS_VERSION
test -s $LOCALBIN/controller-gen || GOARCH=$(go env GOARCH) GOBIN=$LOCALBIN go install sigs.k8s.io/controller-tools/cmd/controller-gen@$CONTROLLER_TOOLS_VERSION
test -s $LOCALBIN/setup-envtest || GOARCH=$(go env GOARCH) GOBIN=$LOCALBIN go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest

go mod download
go mod tidy

# Download kubebuilder assets
echo "Downloading kubebuilder assets for testing..."
$LOCALBIN/setup-envtest use -p path 1.28.0 >/dev/null
 
# Get the path to kubebuilder assets
KUBEBUILDER_ASSETS=$($LOCALBIN/setup-envtest use -p path 1.28.0)

# Print configuration instructions
echo ""
echo "===== Development Environment Setup Complete ====="
echo ""
echo "KUBEBUILDER_ASSETS has been set to: $KUBEBUILDER_ASSETS"
echo ""
echo "For your current session:"
echo "  export KUBEBUILDER_ASSETS=\"$KUBEBUILDER_ASSETS\""
echo ""
echo "To make this permanent, add to your ~/.bashrc, ~/.zshrc, or equivalent like this:"
echo "# For Bash users:"
echo "  echo 'export KUBEBUILDER_ASSETS=\"$KUBEBUILDER_ASSETS\"' >> ~/.bashrc"
echo "# For Zsh users:"
echo "  echo 'export KUBEBUILDER_ASSETS=\"$KUBEBUILDER_ASSETS\"' >> ~/.zshrc"
echo ""
echo "You can now run:"
echo "  make build  - Build the operator"
echo "  make test   - Run tests (automatically sets KUBEBUILDER_ASSETS)"
echo "  make run    - Run the operator locally"
echo "" 