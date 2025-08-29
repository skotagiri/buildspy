# BuildSpy Makefile
.PHONY: all clean buildspy buildspyd deps test install

# Default target builds both binaries
all: buildspy buildspyd

# Clean build artifacts
clean:
	rm -f buildspy buildspyd
	rm -rf cmd/buildspy/buildspy cmd/buildspyd/buildspyd

# Download dependencies
deps:
	go mod tidy
	cd cmd/buildspy && go mod tidy
	cd cmd/buildspyd && go mod tidy

# Build buildspy CLI tool
buildspy:
	cd cmd/buildspy && go build -o ../../buildspy .

# Build buildspyd daemon
buildspyd:
	cd cmd/buildspyd && go build -o ../../buildspyd .

# Install binaries to system
install: buildspy buildspyd
	install -m 755 buildspy /usr/local/bin/
	install -m 755 buildspyd /usr/local/bin/

# Run tests (placeholder for future tests)
test:
	go test ./...
	cd cmd/buildspy && go test ./...
	cd cmd/buildspyd && go test ./...

# Development targets
dev-buildspy:
	cd cmd/buildspy && go run . $(ARGS)

dev-buildspyd:
	cd cmd/buildspyd && go run . $(ARGS)

# Help target
help:
	@echo "BuildSpy Build System"
	@echo ""
	@echo "Targets:"
	@echo "  all        Build both buildspy and buildspyd binaries"
	@echo "  buildspy   Build only the buildspy CLI tool"
	@echo "  buildspyd  Build only the buildspyd daemon"
	@echo "  deps       Download and update dependencies"
	@echo "  clean      Remove build artifacts"
	@echo "  install    Install binaries to /usr/local/bin"
	@echo "  test       Run tests"
	@echo "  help       Show this help message"
	@echo ""
	@echo "Development:"
	@echo "  dev-buildspy   Run buildspy in development mode"
	@echo "  dev-buildspyd  Run buildspyd in development mode"
	@echo ""
	@echo "Example usage:"
	@echo "  make all                    # Build both tools"
	@echo "  make dev-buildspy ARGS='-cmd \"make test\"'"
	@echo "  make dev-buildspyd ARGS='-port 9090'"