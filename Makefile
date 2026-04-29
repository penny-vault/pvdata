EXECUTABLE_NAME = pvdata

GO_MODULE:=$$(go list -m)
GIT_VERSION:=$$(git describe --always)
COMMIT_HASH:=$$(git rev-parse --short HEAD)
BUILD_DATE:=$$(date -Iseconds)

.PHONY: build
build:
	go build -o ${EXECUTABLE_NAME} -ldflags "-X $(GO_MODULE)/pkginfo.Version=$(GIT_VERSION) -X $(GO_MODULE)/pkginfo.BuildDate=$(BUILD_DATE) -X $(GO_MODULE)/pkginfo.CommitHash=$(COMMIT_HASH)"

.PHONY: build-ui
build-ui:
	cd web/ui && npm ci && npm run build

.PHONY: build-all
build-all: build-ui build

.PHONY: install
install:
	go install

.PHONY: lint
lint:
	test -z `go fmt ./...`
	go vet ./...
	golangci-lint run

.PHONY: test
test:
	ginkgo run -race ./...

cyclo:
	gocyclo -ignore ".go/" -ignore "vendor/" -over 20 ../mint

.PHONY: clean
clean:
	rm -rf target
	go clean ./...
