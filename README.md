# Quix Environment Operator

A Kubernetes controller for secure, declarative provisioning of isolated application environments. Automates namespace creation, RBAC scoping, and policy enforcement via CRD.

**Note:** This project is open source and can be used by anyone, but it is primarily designed for use within the Quix ecosystem.

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

Install a specific version
```
operator_version=0.1.2
operator_env_regex=""
helm repo add quix-environment-operator https://quixio.github.io/quix-environment-operator/ && helm repo update
helm pull quix-environment-operator/quix-environment-operator --version $operator_version
helm upgrade --install quix-environment-operator -n quix-operator --create-namespace ./quix-environment-operator-$operator_version.tgz --set env.environmentRegex="$operator_env_regex"
```
For all configuration options see [values.yaml](deploy/quix-environment-operator/values.yaml).

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

## Trademark Notice

"Quix" and the "Quix" logo are trademarks of Quix Analytics Ltd.  
This project is maintained by Quix Analytics Ltd.  
You may not use the "Quix" name or logo in derived projects without prior written permission.