# GitHub Workflows

## Build and Push Workflow

The `build.yml` workflow handles testing, building, and publishing Docker images and Helm charts. It performs the following steps:

1. Runs tests in Docker to ensure code quality
2. Builds the Docker image 
3. Pushes the Docker image to Docker Hub
4. Packages and pushes the Helm chart as an OCI artifact

### Version Handling

The workflow handles versioning differently based on the branch:

**For `main` branch:**
- Docker image is tagged with:
  - `latest`
  - Version from `appVersion` in `Chart.yaml`
- Helm chart is published with version from `Chart.yaml`

**For non-main branches:**
- Both `version` and `appVersion` in `Chart.yaml` are temporarily replaced with `0.0.{BUILD_NUMBER}`
- Docker image is tagged only with the development version (no `latest` tag)
- Helm chart is published with the development version

This prevents development builds from overwriting production releases.

### Required Secrets

You need to add the following secrets to your GitHub repository:

- `DOCKERHUB_USERNAME`: Your Docker Hub username
- `DOCKERHUB_TOKEN`: A Docker Hub personal access token with push permissions

### When is it triggered?

The workflow runs on:
- Push to the `main` branch
- Pull requests targeting the `main` branch
- Manual trigger via GitHub UI

Note: Docker image push and Helm chart push only happen on push to `main` or manual trigger, not on pull requests. 