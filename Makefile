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
	@echo "TODO: Observability v1 offline smoke will be wired by tasks T019-T031."
	@echo "This placeholder does not require real observability platform credentials."
