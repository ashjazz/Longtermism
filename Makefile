.PHONY: test test-race vet eval-smoke

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

eval-smoke:
	@printf '%s\n' 'TODO: eval-smoke is not implemented yet. Complete P0-E tasks T051-T053 before enabling this gate.'
	@exit 1
