FUZZ_TIME ?= 30s
FUZZ_PARALLEL ?= 2
FUZZ_TARGETS := FuzzEvaluate FuzzHistory FuzzScoreSignals

.PHONY: test vet lint lint-fix vuln fuzz fmt

test:
	GOWORK=off go test -race -shuffle=on -count=1 ./...

vet:
	GOWORK=off go vet ./...

lint:
	golangci-lint run ./...

lint-fix:
	golangci-lint run --fix ./...

vuln:
	GOWORK=off go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

fuzz:
	@set -e; for target in $(FUZZ_TARGETS); do \
		GOWORK=off go test -run='^$$' -fuzz="^$$target$$" -fuzztime='$(FUZZ_TIME)' -parallel='$(FUZZ_PARALLEL)' .; \
	done

fmt:
	golangci-lint fmt ./...
