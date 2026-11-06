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
operator_version=0.1.6
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

## Qualified trust-overlay releases

`azure-pipelines-hotfix2.yml` is a manual producer. It defaults to validation;
`publish: true` promotes the exact qualified OCI archive and chart after review.
The image tag is `<Build.BuildNumber>-2026-09-spaces-hotfix-2`; the Helm version is
`0.1.<Build.BuildId>-2026-09-spaces-hotfix-2`. The chart's appVersion is the image
tag. Publication checks both destinations for collisions, fails closed on registry
errors, and verifies remote image and chart digests. It never writes `latest`.

The runtime is the pinned published operator 0.1.6 image, with only its CA bundle
filtered by DER SHA-256 identity. The retained controller, runtime files, UID,
entrypoint and Kubernetes authentication remain unchanged. Source provenance for
that image is unavailable; the chart0.1.6 source tag is used for source tests, not
claimed as a reproducible binary origin. The published chart0.1.6 archive supplies
all templates, including CRD and RBAC, unchanged. The baseline identities are in
`trust/baseline.json`.

The packaged chart selects `quixcontainerregistry.azurecr.io/quix-environment-operator`
and the qualified image tag by default. Explicit `image.repository`/`image.tag`
settings still win. BYOC `environmentOperator.registry` and
`environmentOperator.imageVersion` overrides must be reported by the installer as
qualification exceptions; this release does not qualify or silently replace those
images. In particular, an observed 0.1.0 override is separate from the chart's
0.1.6 default.

The native job requires the `SpotScaleSet-dev` admitted Linux amd64 worker,
Docker/buildx, Go 1.24.2 or later, Python 3.12 or later, Helm, Git and Make. It uses
pinned crane and skopeo containers with the admitted worker CA and read-only
registry credentials. A job-owned docker-container builder uses the admitted
BuildKit image pinned in `build/with-builder.sh`, checks its runtime image identity,
and is removed after qualification. OCI export and provenance remain enabled.
Azure must authorize this GitHub source through its service
connection, the pinned `Quix/Infrastructure` and `Quix/BuildSteps` repository
resources, and the `quixcontainerregistry` service connection. No host package
installation or fabricated worker admission is used.

`make hotfix2-check` runs lightweight release-guard and chart checks. Native
`make hotfix2-validate` also runs existing Docker unit/integration tests, checks the
final filesystem against the original, exercises the retained operator against a
fake Kubernetes API with explicit CA data and unknown-root/hostname failures,
checks approved public TLS through Go's OS store, verifies twelve captured denied
chains at their valid fixture time with original-store success controls, and runs
the pinned shared scanner. Its existing severity policy reports medium/high/critical
findings as warnings; scanner execution and evidence failures block publication.
The source tests use disposable envtest, never a live cluster. Receipts and logs
are retained in `operator-qualification`; missing tests, TLS, scan or digest evidence
must be resolved before release. `make hotfix2-publish` additionally requires
`PUBLISH=true` and the same clean source revision and qualified artifact hashes.

Ordinary source builds and source-test containers use digest-pinned Go 1.24.2
Debian inputs with the reviewed trust filter applied offline before tool or module
downloads. The test image is loaded from the selected buildx builder and executed
by its fresh image ID. The Debian test base provides the sanitation and build
tools without a package-install bootstrap using unfiltered trust.

`make docker-build` filters the pinned distroless runtime bundle, exports the
built image by ID, and checks its actual final trust stores before assigning
`IMG`. `make docker-push` repeats that final-store check before publication.
These source-store checks require Python 3 and do not replace the complete
retained-image TLS, scanner and functional qualification gates above.
