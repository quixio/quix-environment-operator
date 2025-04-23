all: build

# Build the operator binary, ensuring generated code is up-to-date
build: generate-all
	go build -o bin/operator main.go

# Build a docker image for the operator
docker-build:
	@echo "Building Docker image..."
	docker build -t quix-environment-operator:latest .

# Push the docker image to a registry - must specify REGISTRY env var
docker-push:
	@echo "Pushing Docker image to registry..."
	@[ "${REGISTRY}" ] || (echo "Error: REGISTRY environment variable not set"; exit 1)
	docker tag quix-environment-operator:latest ${REGISTRY}/quix-environment-operator:latest
	docker push ${REGISTRY}/quix-environment-operator:latest

# Install the operator via Helm
helm-install: generate-crds
	@echo "Installing operator via Helm..."
	helm upgrade --install quix-environment-operator ./helm/quix-environment-operator \
		--create-namespace --namespace quix-environment

# Uninstall the operator via Helm
helm-uninstall:
	@echo "Uninstalling operator via Helm..."
	helm uninstall quix-environment-operator -n quix-environment

# Package the Helm chart
helm-package:
	@echo "Packaging Helm chart..."
	helm package ./helm/quix-environment-operator -d ./helm

# Validate the Helm chart
helm-lint:
	@echo "Linting Helm chart..."
	helm lint ./helm/quix-environment-operator

# Deploy complete workflow: build, push, and install
deploy: generate-all docker-build docker-push helm-install

# Set up a local kind cluster for development
kind-setup:
	@echo "Setting up kind cluster for development..."
	@which kind > /dev/null || (echo "kind not found, please install kind first: https://kind.sigs.k8s.io/docs/user/quick-start/#installation" && exit 1)
	kind create cluster --name quix-operator || echo "Cluster already exists"
	kubectl cluster-info

# Delete the kind cluster
kind-delete:
	@echo "Deleting kind cluster..."
	kind delete cluster --name quix-operator

# Set up the complete development environment
setup-dev:
	@echo "Setting up development environment..."
	@chmod +x hack/setup-dev-env.sh
	@hack/setup-dev-env.sh

# Install development dependencies
dev-deps:
	go get -d k8s.io/code-generator
	go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
	go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.14.0

# Generate all code and CRDs in one command
generate-all: generate generate-crds

# Generate code (DeepCopy methods)
generate:
	@echo "Generating DeepCopy methods using controller-gen..."
	@$(shell go env GOPATH)/bin/controller-gen \
		object:headerFile="hack/boilerplate.go.txt" \
		paths="./api/..."
	@echo "DeepCopy methods generated successfully"

# Generate CRD yaml files from Go types
generate-crds:
	@echo "Regenerating CRD files..."
	@mkdir -p config/crd/bases
	@rm -f config/crd/bases/*.yaml
	@echo "Installing controller-gen at exact version v0.14.0..."
	@go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.14.0
	@echo "Generating CRDs using controller-gen..."
	@$(shell go env GOPATH)/bin/controller-gen \
		crd:crdVersions=v1 \
		rbac:roleName=manager \
		paths="./api/..." \
		output:crd:artifacts:config=config/crd/bases
	@echo "CRD files regenerated successfully"

# Test commands
test: 
	$(eval export KUBEBUILDER_ASSETS=$(shell $(shell go env GOPATH)/bin/setup-envtest use -p path 1.28.0))
	@echo "Using KUBEBUILDER_ASSETS: $(KUBEBUILDER_ASSETS)"
	make test-unit test-integration

test-unit:
	go test ./... -v -coverprofile=cover.out

test-integration:
	go test ./controllers -v -coverprofile=cover-integration.out -tags=integration

docker-test:
	@echo "Running all tests in Docker container..."
	@chmod +x run-tests.sh
	./run-tests.sh all

tidy:
	go mod tidy

.PHONY: all build docker-build docker-push helm-install helm-uninstall helm-package helm-lint deploy kind-setup kind-delete generate-all generate generate-crds test test-unit test-integration docker-test tidy setup-dev
