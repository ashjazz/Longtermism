.PHONY: test test-race vet eval-smoke obs-smoke

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

eval-smoke:
	go run ./cmd/eval-smoke

obs-smoke:
	go test ./internal/cmd -count=1
	go test ./internal/eval/smoke -count=1
	go test ./pkg/ai/obs ./pkg/ai/obs/testutil -count=1
