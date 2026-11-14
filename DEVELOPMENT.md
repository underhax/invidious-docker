# Invidious Docker Development Guide

## Prerequisites

Ensure the following dependencies are installed prior to local development:
- Git
- Go 1.27+
- GNU Make
- golangci-lint (required for strict Go code linting)
- govulncheck (required for Go vulnerability checking)
- hadolint (required for `Dockerfile` and `Dockerfile.arm64` linting)
- trivy (required for container and filesystem security scanning)
- gitleaks (required for repository secret scanning)

## Repository Initialization

1. **Clone the repository:**
   ```sh
   git clone https://github.com/underhax/invidious-docker
   cd invidious-docker
   ```

2. **Install Go and Security Tooling:**
   Install the required Go linter, vulnerability checker, and security scanners using any convenient method for your platform. These tools are utilized in our CI pipeline and are required for local code validation prior to submission:
   - **golangci-lint**: [https://golangci-lint.run/docs/welcome/install/](https://golangci-lint.run/docs/welcome/install/)
   - **govulncheck**: `go install golang.org/x/vuln/cmd/govulncheck@latest`
   - **hadolint**: [https://github.com/hadolint/hadolint#install](https://github.com/hadolint/hadolint#install)
   - **trivy**: [https://trivy.dev/docs/latest/getting-started/](https://trivy.dev/docs/latest/getting-started/)
   - **gitleaks**: [https://github.com/gitleaks/gitleaks#installing](https://github.com/gitleaks/gitleaks#installing)

3. **Repository Layout:**
   ```text
   ├── Dockerfile                     # Multi-stage scratch build (linux/amd64)
   ├── Dockerfile.arm64               # Multi-stage scratch build (linux/arm64)
   ├── docker-compose.yaml            # Production deployment manifest
   ├── Makefile                       # Verification, linting, and testing targets
   ├── .golangci.yml                  # Strict Go linter configuration
   ├── go.mod                         # Go module definition
   ├── examples/                      # Environment variable templates
   │   ├── .env.example               # Host UID/GID and image version overrides
   │   ├── companion.env.example      # Invidious Companion environment template
   │   └── invidious.env.example      # Invidious server environment template
   ├── init/                          # Static Go entrypoint (secret mediation, UDS bridge & env scrub)
   │   ├── main.go
   │   └── main_test.go
   └── probe/                         # Static Go healthcheck probe (HTTP stats & companion UDS)
       ├── main.go
       └── main_test.go
   ```

## Code Validation & Testing

The project enforces strict code quality and security standards across the Go helper binaries (`init`, `probe`), `Dockerfile`, `Dockerfile.arm64`, and Compose manifests. All validation steps are centralized in the [`Makefile`](Makefile).

1. **Go Formatting, Linting & Unit Tests:**
   To validate Go code formatting (`gofmt -s`), execute `golangci-lint`, verify compilation, and run race-enabled unit tests:
   ```sh
   make check
   make test
   ```

2. **Vulnerability & Secret Scanning:**
   To execute Go vulnerability checks, filesystem security scans, and repository secret detection:
   ```sh
   make vulncheck
   make security-trivy
   make secrets-scan
   ```

3. **Docker Linting:**
   To validate `Dockerfile` and `Dockerfile.arm64` best practices via `hadolint`:
   ```sh
   make docker-lint
   ```

4. **Code Coverage:**
   To execute the Go test suite with the race detector enabled and generate a function-level coverage report:
   ```sh
   make coverage
   ```
   To remove generated coverage artifacts afterward:
   ```sh
   make clean
   ```

5. **Project Verification:**
   Prior to submitting any pull request, you must execute the complete validation, testing, and security suite:
   ```sh
   make verify
   ```

## Build Workflow

The repository builds zero-dependency static Go binaries (`init` and `probe` with `CGO_ENABLED=0`) and packages them alongside the upstream `invidious` and `invidious_companion` binaries into minimal `FROM scratch` container targets (`invidious` and `companion`).

1. **Compile the Go binaries locally:**
   ```sh
   CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath -o /dev/null ./init
   CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath -o /dev/null ./probe
   ```

2. **Cross-compile the Go binaries for target container architectures (Optional):**
   You can verify cross-compilation for the supported Linux container architectures (`amd64` and `arm64`) by specifying `GOOS` and `GOARCH`:

   **Linux (AMD64):**
   ```sh
   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -trimpath -o /dev/null ./init
   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -trimpath -o /dev/null ./probe
   ```

   **Linux (ARM64):**
   ```sh
   CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -trimpath -o /dev/null ./init
   CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -trimpath -o /dev/null ./probe
   ```

3. **Build the Docker images:**
   Build the `invidious` and `companion` scratch image targets natively on AMD64 using `Dockerfile`:
   ```sh
   docker build --target invidious -t invidious:dev .
   docker build --target companion -t invidious-companion:dev .
   ```

   To build explicitly for a specific architecture on a Docker Engine with BuildKit (using `Dockerfile` for `linux/amd64` and `Dockerfile.arm64` for `linux/arm64`):
   ```sh
   # For AMD64 architecture:
   docker build --platform linux/amd64 --target invidious -t invidious:dev -f Dockerfile .
   docker build --platform linux/amd64 --target companion -t invidious-companion:dev -f Dockerfile .

   # For ARM64 architecture:
   docker build --platform linux/arm64 --target invidious -t invidious:dev -f Dockerfile.arm64 .
   docker build --platform linux/arm64 --target companion -t invidious-companion:dev -f Dockerfile.arm64 .
   ```

## Makefile Targets Reference

| Target | Description |
| --- | --- |
| `make check` | Validates Go code formatting (`gofmt -s`), executes `golangci-lint`, and verifies compilation across `./init` and `./probe`. |
| `make test` | Executes the Go unit test suite across all packages with race condition detection enabled (`-race`). |
| `make coverage` | Executes the Go unit test suite with race detection and outputs a function-level statement coverage report (`coverage.out`). |
| `make vulncheck` | Analyzes Go source code and standard library dependencies for known vulnerabilities using `govulncheck`. |
| `make docker-lint` | Lints `Dockerfile` and `Dockerfile.arm64` via `hadolint`. |
| `make security-trivy` | Scans the repository filesystem for `CRITICAL` and `HIGH` severity vulnerabilities using `trivy`. |
| `make secrets-scan` | Scans the repository working tree for hardcoded secrets using `gitleaks`. |
| `make verify` | Sequentially executes `check`, `test`, `vulncheck`, `docker-lint`, `security-trivy`, and `secrets-scan`. **Must be executed prior to submitting a pull request.** |
| `make clean` | Removes generated test coverage artifacts (`coverage` and `coverage.out`). |
