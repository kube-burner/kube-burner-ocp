
.PHONY: build lint clean test help all setup-envtest test-ra-integration


ARCH ?= $(shell uname -m | sed s/aarch64/arm64/ | sed s/x86_64/amd64/)
BIN_NAME = kube-burner-ocp
BIN_DIR = bin
BIN_PATH = $(BIN_DIR)/$(ARCH)/$(BIN_NAME)
CGO = 0
TEST_BINARY ?= $(CURDIR)/$(BIN_PATH)
ENVTEST_K8S_VERSION ?= 1.35.0
# Pinned controller-runtime release-0.23 setup-envtest tool.
ENVTEST_SETUP_VERSION ?= f9589b9f2b9d
ENVTEST_BIN_DIR = $(abspath $(BIN_DIR)/envtest)
RA_INTEGRATION_BINARY = $(abspath $(BIN_DIR)/$(ARCH)/ra-latency-integration.test)
# Rootless user namespaces work on many Linux hosts. Use sudo here when required.
RA_INTEGRATION_RUNNER ?=

GIT_COMMIT = $(shell git rev-parse HEAD)
VERSION ?= $(shell hack/tag_name.sh)

# Bats test filter tags computed from the changed workload code paths. Set to an
# empty value (e.g. FILTER_TAGS=) to force running the whole suite.
FILTER_TAGS ?= $(shell hack/get_changed_labels.py hack/bats_test_mappings.yml)
SOURCES := $(shell find . -type f -name "*.go")
SOURCES += $(shell find cmd/config/)
BUILD_DATE = $(shell date '+%Y-%m-%d-%H:%M:%S')
VERSION_PKG=github.com/cloud-bulldozer/go-commons/v2/version

all: lint build

help:
	@echo "Commands for $(BIN_PATH):"
	@echo
	@echo 'Usage:'
	@echo '    make lint                     Install and execute pre-commit'
	@echo '    make clean                    Clean the compiled binaries'
	@echo '    [ARCH=arch] make build        Compile the project for arch, default amd64'
	@echo '    [ARCH=arch] make install      Installs kube-burner binary in the system, default amd64'
	@echo '    make help                     Show this message'
	@echo '    make test-ra-integration      Run isolated RA API/netlink/ICMP tests (Linux)'

build: $(BIN_PATH)

$(BIN_PATH): go.sum $(SOURCES)
	@echo -e "\033[2mBuilding $(BIN_PATH)\033[0m"
	@echo "GOPATH=$(GOPATH)"
	GOARCH=$(ARCH) CGO_ENABLED=$(CGO) go build -v -ldflags "-X $(VERSION_PKG).GitCommit=$(GIT_COMMIT) -X $(VERSION_PKG).BuildDate=$(BUILD_DATE) -X $(VERSION_PKG).Version=$(VERSION)" -o $(BIN_PATH) ./cmd/

lint:
	@echo "Executing pre-commit for all files"
	pre-commit run --all-files
	@echo "pre-commit executed."

clean:
	test ! -e $(BIN_DIR) || rm -Rf $(BIN_PATH)

install:
	cp $(BIN_PATH) /usr/bin/$(BIN_NAME)

test: test-ocp

test-ocp:
	cd test && KUBE_BURNER_OCP=$(TEST_BINARY) bats $(if $(TEST_FILTER),--filter "$(TEST_FILTER)",) -F pretty -T --print-output-on-failure test-ocp.bats $(FILTER_TAGS)

$(BIN_DIR)/setup-envtest:
	mkdir -p $(BIN_DIR)
	GOBIN=$(abspath $(BIN_DIR)) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_SETUP_VERSION)

setup-envtest: $(BIN_DIR)/setup-envtest
	$(BIN_DIR)/setup-envtest use $(ENVTEST_K8S_VERSION) --bin-dir $(ENVTEST_BIN_DIR)

test-ra-integration: setup-envtest
	mkdir -p $(dir $(RA_INTEGRATION_BINARY))
	CGO_ENABLED=0 go test -c -tags=integration -o $(RA_INTEGRATION_BINARY) ./pkg/measurements
	KUBEBUILDER_ASSETS="$$($(BIN_DIR)/setup-envtest use $(ENVTEST_K8S_VERSION) --bin-dir $(ENVTEST_BIN_DIR) -i -p path)" $(RA_INTEGRATION_RUNNER) $(RA_INTEGRATION_BINARY) -test.v -test.run '^TestRaLatencyIntegration$$' -test.timeout 5m
