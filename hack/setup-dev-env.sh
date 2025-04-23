#!/bin/bash

# This script sets up the development environment for the Quix Environment Operator.
# It installs required tools and configures environment variables.

set -e

# Install sigs.k8s.io/controller-runtime/tools/setup-envtest
echo "Installing setup-envtest..."
go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest

# Add GOPATH/bin to PATH if not already there to ensure setup-envtest is available
if [[ ":$PATH:" != *":$HOME/go/bin:"* ]]; then
    export PATH="$HOME/go/bin:$PATH"
    echo "Added $HOME/go/bin to PATH"
fi

# Download kubebuilder assets
echo "Downloading kubebuilder assets for testing..."
setup-envtest use -p path 1.28.0 >/dev/null

# Get the path to kubebuilder assets
KUBEBUILDER_ASSETS=$(setup-envtest use -p path 1.28.0)

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