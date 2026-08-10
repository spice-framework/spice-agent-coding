.PHONY: tools-bootstrap fast check coverage fmt verify verify-release verify-release-artifacts dev-daemon dev-terminal

tools-bootstrap:
	go run ./internal/qualitygate -mode=tools-bootstrap

fast:
	go run ./internal/qualitygate -mode=fast

check:
	go run ./internal/qualitygate -mode=check

coverage:
	go run ./internal/qualitygate -mode=coverage

fmt:
	go run ./internal/qualitygate -mode=fmt

verify:
	go run ./internal/qualitygate -mode=verify

verify-release: verify

verify-release-artifacts:
	go run ./internal/qualitygate -mode=release-artifacts -artifacts="$(SPICE_AGENT_VERIFIED_ARTIFACT_DIR)"

dev-daemon dev-terminal: export GOWORK := off
dev-daemon dev-terminal: export GOTOOLCHAIN := local
dev-daemon dev-terminal: export GOFLAGS := -mod=vendor
dev-daemon dev-terminal: export GOPROXY := off
dev-daemon dev-terminal: export GOSUMDB := off

# The daemon and terminal use separate Spice supervisors. Target-local source
# changes therefore rebuild only their owning process, while shared source and
# module changes correctly invalidate both graphs.
dev-daemon:
	go tool github.com/spice-framework/toolchain/cmd/spice dev --target spice-agentd --exclude=cmd/spice-agent/** --exclude=internal/terminal/** --exclude=internal/terminalcommand/** --exclude=internal/terminalconnector/** --exclude=internal/tuisession/** --exclude=internal/spicegen/spice_agent/** --exclude=.spice/spice_agent.manifest.json --exclude=internal/architectureproof/** ./cmd/spice-agentd -- serve

dev-terminal:
	go tool github.com/spice-framework/toolchain/cmd/spice dev --target spice-agent --exclude=cmd/spice-agentd/** --exclude=internal/daemon/** --exclude=internal/daemoncommand/** --exclude=internal/spicegen/spice_agentd/** --exclude=.spice/spice_agentd.manifest.json --exclude=internal/architectureproof/** ./cmd/spice-agent
