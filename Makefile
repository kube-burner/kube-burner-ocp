
.PHONY: build lint clean replace-imports test help all


ARCH ?= amd64
BIN_NAME = kube-burner-ocp
BIN_DIR = bin
BIN_PATH = $(BIN_DIR)/$(ARCH)/$(BIN_NAME)
CGO = 0

GITHUB_USERNAME := $(shell git remote get-url origin | sed -n 's#.*/\([^.]*\)/[^/]*\.git#\1#p' 2>/dev/null)
GIT_COMMIT = $(shell git rev-parse HEAD)
VERSION ?= $(shell hack/tag_name.sh)
SOURCES := $(shell find . -type f -name "*.go")
BUILD_DATE = $(shell date '+%Y-%m-%d-%H:%M:%S')
VERSION_PKG=github.com/cloud-bulldozer/go-commons/version
ifeq ($(strip $(GITHUB_USERNAME)),)
    GITHUB_USERNAME := kube-burner
endif
SED_COMMAND := sed -i 's/github.com\/kube-burner\/kube-burner/github.com\/$(GITHUB_USERNAME)\/kube-burner/g'

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

build: $(BIN_PATH)

$(BIN_PATH): $(SOURCES)
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

replace-imports:
	find . -type f -name "*.go" -exec $(SED_COMMAND) {} +
	find . -type f -name "go.mod" -exec $(SED_COMMAND) {} +

test: test-ocp

test-ocp:
	cd test && bats -F pretty -T --print-output-on-failure test-ocp.bats
