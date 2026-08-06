.PHONY: tools-bootstrap fast check fmt verify live-acceptance

tools-bootstrap:
	go run ./internal/qualitygate -mode=tools-bootstrap

fast:
	go run ./internal/qualitygate -mode=fast

check:
	go run ./internal/qualitygate -mode=check

fmt:
	go run ./internal/qualitygate -mode=fmt

verify:
	go run ./internal/qualitygate -mode=verify

live-acceptance:
	go test -tags=openai_live -run '^TestLiveOpenAIResponse$$' -count=1 .
