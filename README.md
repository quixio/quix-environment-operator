# Quix Environment Operator

A Kubernetes controller for secure, declarative provisioning of isolated application environments. Automates namespace creation, RBAC scoping, and policy enforcement via CRD.

**Note:** Open source for transparency, not for general use outside the Quix ecosystem.

## Features

- Declarative environment definition via custom `Environment` resource
- Automated namespace creation with strict naming conventions
- Centralized ServiceAccounts with precise permissions
- Audit-friendly event emission
- Namespace-scoped actions with least-privilege security
- More in [Design docs](/docs/CONTROLLER_DESIGN.md)

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

# Run tests in Docker (consistent environment)
make docker-test
```

### Workflow

1. `make setup-dev` - Install required tools
2. `make build` - Build binary and generate files
3. Make code changes
4. `make test` - Verify changes
5. `make help` for the rest
6. GitHub build actions are explained [here](.github/GHACTION_README.md)

## Contributing

Please see [CONTRIBUTING.md](CONTRIBUTING.md) for details on how to contribute to this project. All external contributions must be submitted through forks.

## License

[Apache 2.0 License](./LICENSE)  

Remember that "Quix" and the "Quix" logo are trademarks of Quix Analytics Ltd. Your contributions must respect the trademark restrictions outlined in the [NOTICE](./NOTICE) file. 

## Trademark Notice

"Quix" and the "Quix" logo are trademarks of Quix Analytics Ltd.  
This project is maintained by Quix Analytics Ltd.  
You may not use the "Quix" name or logo in derived projects without prior written permission.