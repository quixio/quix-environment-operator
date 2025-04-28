# Image URL to use all building/pushing image targets
IMG ?= quix-environment-operator:latest
# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION = 1.26.0

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk commands is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate CRDs and RBAC manifests
	@echo "Cleaning CRD directory..."
	@mkdir -p config/crd/bases
	@rm -f config/crd/bases/*.yaml
	@echo "Generating CRDs using controller-gen..."
	$(LOCALBIN)/controller-gen crd:crdVersions=v1 rbac:roleName=manager-role paths="./api/..." output:crd:artifacts:config=config/crd/bases
	@echo "CRD generation complete."

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	@echo "Generating DeepCopy code..."
	$(LOCALBIN)/controller-gen object:headerFile="hack/boilerplate.go.txt" paths="./api/..."
	@echo "DeepCopy code generation complete."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet envtest ## Run tests.
	$(eval export KUBEBUILDER_ASSETS=$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path))
	@echo "Using KUBEBUILDER_ASSETS: $(KUBEBUILDER_ASSETS)"
	make test-unit test-integration

.PHONY: test-unit
test-unit: envtest ## Run unit tests.
	$(eval export KUBEBUILDER_ASSETS=$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path))
	go test ./... -v -coverprofile=cover.out

.PHONY: test-integration
test-integration: envtest manifests ## Run integration tests.
	$(eval export KUBEBUILDER_ASSETS=$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path))
	go test ./... -v -coverprofile=cover-integration.out -tags=integration

.PHONY: docker-test
docker-test: ## Run all tests in Docker container.
	@echo "Running all tests in Docker container..."
	@if ! command -v docker &> /dev/null; then \
		echo "Error: Docker is required but not found. Please install Docker first."; \
		exit 1; \
	fi
	@chmod +x build/run-tests.sh
	./build/run-tests.sh all

.PHONY: docker-test-unit
docker-test-unit: ## Run only unit tests in Docker container.
	@echo "Running unit tests in Docker container..."
	@if ! command -v docker &> /dev/null; then \
		echo "Error: Docker is required but not found. Please install Docker first."; \
		exit 1; \
	fi
	@chmod +x build/run-tests.sh
	./build/run-tests.sh unit

.PHONY: docker-test-integration
docker-test-integration: ## Run only integration tests in Docker container.
	@echo "Running integration tests in Docker container..."
	@if ! command -v docker &> /dev/null; then \
		echo "Error: Docker is required but not found. Please install Docker first."; \
		exit 1; \
	fi
	@chmod +x build/run-tests.sh
	./build/run-tests.sh integration

##@ Build

.PHONY: generate-crds
generate-crds: controller-gen ## Generate CRD files
	@echo "Regenerating CRD files..."
	@mkdir -p config/crd/bases
	@rm -f config/crd/bases/*.yaml
	@echo "Generating CRDs using controller-gen..."
	$(LOCALBIN)/controller-gen \
		crd:crdVersions=v1 \
		paths="./api/..." \
		output:crd:artifacts:config=config/crd/bases
	@echo "CRD files regenerated successfully"

.PHONY: helm-copy-crds
helm-copy-crds: generate-crds ## Copy CRDs to Helm chart templates directory
	@echo "Copying CRDs to Helm chart templates directory..."
	@mkdir -p helm/quix-environment-operator/templates
	cp config/crd/bases/quix.io_environments.yaml deploy/quix-environment-operator/templates/crds.yaml
	@echo "CRDs copied successfully"

.PHONY: build
build: generate-crds generate fmt vet helm-copy-crds ## Build manager binary.
	go build -o bin/operator cmd/operator/main.go

# If you wish built the manager image targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64 ). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/develop-images/build_enhancements/
.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	docker build -t ${IMG} . -f build/dockerfile

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	docker push ${IMG}

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	@echo "Current kubectl context: $$(kubectl config current-context)"
	@if [ "$(FORCE)" != "true" ]; then \
		read -p "Continue installing CRDs into this cluster? [y/N] " confirm; \
		if [ "$${confirm}" != "y" ] && [ "$${confirm}" != "Y" ]; then \
			echo "Installation aborted."; \
			exit 1; \
		fi; \
	fi
	kubectl apply -f config/crd/bases/

.PHONY: uninstall
uninstall: ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config.
	@echo "Current kubectl context: $$(kubectl config current-context)"
	@if [ "$(FORCE)" != "true" ]; then \
		read -p "Continue uninstalling CRDs from this cluster? [y/N] " confirm; \
		if [ "$${confirm}" != "y" ] && [ "$${confirm}" != "Y" ]; then \
			echo "Uninstallation aborted."; \
			exit 1; \
		fi; \
	fi
	kubectl delete --ignore-not-found=$(ignore-not-found) -f config/crd/bases/

.PHONY: deploy
deploy: manifests ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	@echo "Current kubectl context: $$(kubectl config current-context)"
	@if [ "$(FORCE)" != "true" ]; then \
		read -p "Continue deploying controller to this cluster? [y/N] " confirm; \
		if [ "$${confirm}" != "y" ] && [ "$${confirm}" != "Y" ]; then \
			echo "Deployment aborted."; \
			exit 1; \
		fi; \
	fi
	helm upgrade --install quix-environment-operator ./deploy/quix-environment-operator \
		--create-namespace --namespace quix-environment \
		--set image.repository=$(shell echo ${IMG} | cut -d: -f1) \
		--set image.tag=$(shell echo ${IMG} | cut -d: -f2)

.PHONY: undeploy
undeploy: ## Undeploy controller from the K8s cluster specified in ~/.kube/config.
	@echo "Current kubectl context: $$(kubectl config current-context)"
	@if [ "$(FORCE)" != "true" ]; then \
		read -p "Continue undeploying controller from this cluster? [y/N] " confirm; \
		if [ "$${confirm}" != "y" ] && [ "$${confirm}" != "Y" ]; then \
			echo "Undeployment aborted."; \
			exit 1; \
		fi; \
	fi
	helm uninstall quix-environment-operator -n quix-environment

.PHONY: helm-package
helm-package: ## Package the Helm chart.
	@echo "Packaging Helm chart..."
	helm package ./deploy/quix-environment-operator -d ./helm

.PHONY: helm-lint
helm-lint: ## Validate the Helm chart.
	@echo "Linting Helm chart..."
	helm lint ./deploy/quix-environment-operator

##@ Build Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
ENVTEST ?= $(LOCALBIN)/setup-envtest

## Tool Versions
CONTROLLER_TOOLS_VERSION ?= v0.14.0
GOIMPORTS_TOOLS_VERSION ?= v0.32.0

.PHONY: controller-gen
controller-gen:  ## Download controller-gen locally if necessary.
	test -s $(LOCALBIN)/controller-gen || GOARCH=$(go env GOARCH) GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION)

.PHONY: envtest
envtest: ## Download envtest-setup locally if necessary.
	test -s $(LOCALBIN)/setup-envtest || GOARCH=$(go env GOARCH) GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest

##@ Development Environment

.PHONY: kind-setup
kind-setup: ## Set up a local kind cluster for development.
	@echo "Setting up kind cluster for development..."
	@which kind > /dev/null || (echo "kind not found, please install kind first: https://kind.sigs.k8s.io/docs/user/quick-start/#installation" && exit 1)
	kind create cluster --name quix-operator || echo "Cluster already exists"
	kubectl cluster-info

.PHONY: kind-delete
kind-delete: ## Delete the kind cluster.
	@echo "Deleting kind cluster..."
	kind delete cluster --name quix-operator

.PHONY: setup-dev
setup-dev: ## Set up the complete development environment.
	@echo "Setting up development environment..."
	@chmod +x hack/setup-dev-env.sh
	@hack/setup-dev-env.sh

.PHONY: dev-deps
dev-deps: ## Install development dependencies.
	go get -d k8s.io/code-generator
	GOARCH=$(go env GOARCH) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
	GOARCH=$(go env GOARCH) go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.14.0

.PHONY: tidy
tidy: fmt ## Run go mod tidy and clean up imports.
	test -s $(LOCALBIN)/goimports || GOARCH=$(go env GOARCH) GOBIN=$(LOCALBIN) go install golang.org/x/tools/cmd/goimports@$(GOIMPORTS_TOOLS_VERSION)
	@echo "Cleaning up imports in Go files..."
	@find . -type f -name "*.go" -not -path "./vendor/*" | xargs $(LOCALBIN)/goimports -w
	@echo "Running go mod tidy..."
	go mod tidy 