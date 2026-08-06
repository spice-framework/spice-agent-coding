.PHONY: fast check verify

fast:
	go run ./internal/qualitygate -mode=fast

check:
	go run ./internal/qualitygate -mode=check

verify:
	go run ./internal/qualitygate -mode=verify
