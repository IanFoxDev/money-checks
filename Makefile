LINT_IMAGE := golangci/golangci-lint:v2.14.0

.PHONY: test vet fmt lint build check

test:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

lint:
	docker run --rm -v $(CURDIR):/src -w /src $(LINT_IMAGE) golangci-lint run

build:
	go build -o bin/money-checks ./cmd/money-checks

check: vet test lint
