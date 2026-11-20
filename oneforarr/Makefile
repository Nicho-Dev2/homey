# Run these from the oneforarr folder.
export CGO_ENABLED := 0

.PHONY: ui build run test vet docker size

# ui type-checks and builds the web interface into app/frontend/dist.
ui:
	npm --prefix app/frontend install
	npm --prefix app/frontend run build

# build makes bin/oneforarr with the web interface inside it.
build: ui
	go build -trimpath -ldflags="-s -w" -o bin/oneforarr ./app/backend/cmd/oneforarr

# run starts the app on http://localhost:7979 with its data in ./config.
# Indexer definition files are read from DEFS (default ./config/definitions).
DEFS ?= ./config/definitions
run: build
	ONEFORARR_CONFIG_DIR=./config ONEFORARR_DEFINITIONS_DIR="$(DEFS)" ./bin/oneforarr

test:
	go test ./...

vet:
	go vet ./...

docker:
	docker build -f deploy/Dockerfile -t oneforarr:dev .

# size shows the image size; the goal is to stay under 15 MB.
size: docker
	docker image ls oneforarr:dev --format '{{.Repository}}:{{.Tag}}  {{.Size}}'
