# Carbon-Aware Kubernetes Scheduler
# GNU Make. Recipe lines MUST start with a tab character.

GO                    ?= go
GOLANGCI              ?= golangci-lint
KIND                  ?= kind
BASH                  ?= bash
DOCKER                ?= docker
BIN_DIR               ?= bin
KIND_CLUSTER_NAME     ?= carbon-aware
KIND_CONFIG           ?= deployments/kind/cluster-config.yaml
IMAGE_TAG             ?= 0.1.0
SCHEDULER_IMAGE       ?= carbon-aware-scheduler:$(IMAGE_TAG)
CARBON_SERVICE_IMAGE  ?= carbon-data-service:$(IMAGE_TAG)
DOCKER_PLATFORM       ?= linux/amd64
DOCKERFILE_SCHEDULER  ?= build/Dockerfile.scheduler
DOCKERFILE_CARBON     ?= build/Dockerfile.carbon-service

export DOCKER_BUILDKIT = 1

.PHONY: all lint test build docker-build kind-load kind-up kind-down clean help

all: lint test build

help:
	@echo "Targets:"
	@echo "  lint          Run golangci-lint on the Go module"
	@echo "  test          Run Go unit tests"
	@echo "  build         Build scheduler and carbon-service binaries"
	@echo "  docker-build  Build distroless images for scheduler and CDS"
	@echo "  kind-load     Load images into the local kind cluster"
	@echo "  kind-up       Create local kind cluster and label energy zones"
	@echo "  kind-down     Delete local kind cluster"
	@echo "  clean         Remove build artifacts"

lint:
	$(GOLANGCI) run ./...

test:
	$(GO) test ./...

build:
	$(GO) build -o $(BIN_DIR)/scheduler$(GOEXE) ./cmd/scheduler
	$(GO) build -o $(BIN_DIR)/carbon-service$(GOEXE) ./cmd/carbon-service

docker-build:
	$(DOCKER) build --platform $(DOCKER_PLATFORM) -f $(DOCKERFILE_CARBON) -t $(CARBON_SERVICE_IMAGE) .
	$(DOCKER) build --platform $(DOCKER_PLATFORM) -f $(DOCKERFILE_SCHEDULER) -t $(SCHEDULER_IMAGE) .

kind-load: docker-build
	$(KIND) load docker-image $(SCHEDULER_IMAGE) --name $(KIND_CLUSTER_NAME)
	$(KIND) load docker-image $(CARBON_SERVICE_IMAGE) --name $(KIND_CLUSTER_NAME)

kind-up:
	KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) KIND_CONFIG=$(KIND_CONFIG) KIND=$(KIND) $(BASH) scripts/setup_kind.sh

kind-down:
	KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) KIND=$(KIND) $(BASH) scripts/teardown_kind.sh

clean:
	$(GO) clean
	rm -rf $(BIN_DIR)
