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

.PHONY: all lint test test-race test-integration e2e-deploy test-e2e test-all build docker-build kind-load kind-up kind-down clean help

all: lint test build

help:
	@echo "Targets:"
	@echo "  lint          Run golangci-lint on the Go module"
	@echo "  test          Run Go unit tests"
	@echo "  test-race     Unit + integration tests with the race detector"
	@echo "  test-integration  CDS <-> plugin cache integration suite (-race)"
	@echo "  e2e-deploy    Apply the E2E overlay (tests/e2e/manifests) to kind"
	@echo "  test-e2e      E2E + chaos suite against the kind cluster"
	@echo "  test-all      test-race then test-e2e, TEST_REPEAT times (default 1)"
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

TEST_REPEAT           ?= 1
E2E_TIMEOUT           ?= 40m

test-race:
	$(GO) test -race -count=1 ./...

test-integration:
	$(GO) test -v -race -count=1 -timeout 10m ./tests/integration/...

e2e-deploy:
	kubectl apply -k tests/e2e/manifests
	kubectl -n carbon-aware rollout status deploy/carbon-data-service --timeout=180s
	kubectl -n carbon-aware rollout status deploy/carbon-aware-scheduler --timeout=180s

test-e2e:
	$(GO) test -v -tags=e2e -count=1 -timeout $(E2E_TIMEOUT) ./tests/e2e/...

test-all:
	@set -e; for i in $$(seq 1 $(TEST_REPEAT)); do \
		echo "=== test-all run $$i/$(TEST_REPEAT)"; \
		$(MAKE) --no-print-directory test-race; \
		$(MAKE) --no-print-directory test-e2e; \
	done

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
