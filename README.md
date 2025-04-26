# Quix Environment Operator

The Quix Environment Operator is a Kubernetes operator that manages Environment resources. It creates and maintains namespaces and role bindings for environments, ensuring proper isolation and permissions for users.

## Overview

The operator watches for Environment custom resources and performs the following actions:

1. Creates a dedicated namespace for each environment
2. Sets up role bindings to grant users access to their environments
3. Manages resource quotas and other configurations
4. Handles cleanup when environments are deleted

## Features

- **Environment Isolation**: Each environment gets its own Kubernetes namespace
- **User Access Management**: Role-based access control for environment users
- **Status Reporting**: Provides status updates for environments and their sub-resources

## Prerequisites

- Kubernetes 1.19+
- kubectl 1.19+
- Go 1.19+ (for development)

## Installation

### Using Kustomize

```bash
kubectl apply -k config/default
```

### Using Helm

```bash
helm repo add quix https://quix-analytics.github.io/charts
helm install quix-environment-operator quix/environment-operator
```

## Usage

### Creating an Environment

```yaml
apiVersion: quix.io/v1
kind: Environment
metadata:
  name: demo-environment
spec:
  id: demo-env
  annotations:
    description: "Demo environment for testing"
```

### Checking Environment Status

```bash
kubectl get environments
kubectl describe environment demo-environment
```

## Development

### Building

```bash
make build
```

### Running Locally

```bash
make run
```

### Running Tests

```bash
make test
```

### Building Container Image

```bash
make docker-build IMG=quix-environment-operator:latest
```

## Project Structure

```
├── api                 # API definitions (CRD)
├── config              # Deployment configurations
├── internal            # Internal packages
│   ├── controller      # Reconciler implementations
│   └── resources       # Resources management (namespace, rolebinding)
└── main.go             # Entry point
```

## Contributing

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/my-feature`)
3. Commit your changes (`git commit -am 'Add my feature'`)
4. Push to the branch (`git push origin feature/my-feature`)
5. Create a new Pull Request

## License

Copyright (c) Quix Analytics. All rights reserved. 