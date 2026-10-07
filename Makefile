LINT_IMAGE := golangci/golangci-lint:v2.14.0
# The database tests run when these are set; make postgres-up starts the database.
export MONEY_CHECKS_TEST_PG_DSN ?= postgres://checks:checks@127.0.0.1:55434/checks

.PHONY: test vet fmt lint build check postgres-up postgres-down

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

postgres-up:
	docker run -d --rm --name money-checks-pg -e POSTGRES_USER=checks -e POSTGRES_PASSWORD=checks -e POSTGRES_DB=checks -p 55434:5432 postgres:17-alpine
	until docker exec money-checks-pg pg_isready -U checks >/dev/null 2>&1; do sleep 1; done

postgres-down:
	docker rm -f money-checks-pg
