# Quix Environment Operator

A Kubernetes controller for secure, declarative provisioning of isolated application environments. Automates namespace creation, RBAC scoping, and policy enforcement via CRD.

**Note:** Open source for transparency, not for general use outside the Quix ecosystem.

## Features

- Declarative environment definition via custom `Environment` resource
- Automated namespace creation with strict naming conventions
- Centralized ServiceAccounts with precise permissions
- Audit-friendly event emission
- Namespace-scoped actions with least-privilege security

## Audience

- Customers hosting Kubernetes clusters for Quix deployments
- Security reviewers
- Platform integration engineers

## Deployment

Packaged as a Helm chart for customer-managed Kubernetes clusters. Operates within pre-approved RBAC constraints.

## Development

### Setup

```bash
git clone https://github.com/quix-analytics/quix-environment-operator.git
cd quix-environment-operator
make setup-dev
make build
```

### Testing

You can run tests locally or in a Docker container:

```bash
# Run tests locally
make test            # All tests
make test-unit       # Unit tests only
make test-integration # Integration tests only

# Run tests in Docker (consistent environment)
make docker-test
```

### Workflow

1. `make setup-dev` - Install required tools
2. `make build` - Build binary and generate files
3. Make code changes
4. `make test` - Verify changes
5. Deploy via Helm for testing

### Commands

- `make build` - Build operator binary
- `make generate-all` - Generate code (DeepCopy methods and CRDs)
- `make test` - Run all tests
- `make test-unit` - Run unit tests
- `make test-integration` - Run integration tests
- `make docker-test` - Run all tests in a Docker container
- `make docker-build` - Build Docker image
- `make helm-install/uninstall` - Install/uninstall via Helm
- `make kind-setup/delete` - Create/delete local kind cluster
- `make tidy` - Tidy Go modules

## License

[Apache 2.0 License](./LICENSE)  
See [NOTICE](./NOTICE) for trademark restrictions.

## Trademark Notice

"Quix" and the "Quix" logo are trademarks of Quix Analytics Ltd.  
This project is maintained by Quix Analytics Ltd.  
You may not use the "Quix" name or logo in derived projects without prior written permission.