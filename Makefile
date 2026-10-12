.PHONY: test build lint e2e
test:
	go test -race ./...
build:
	go build -o bin/homerun ./cmd/homerun
lint:
	go vet ./...
e2e:
	@echo "Manual end-to-end checklist: docs/e2e.md"
