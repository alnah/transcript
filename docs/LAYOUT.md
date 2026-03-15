# Project Layout

See [ARCHITECTURE.md](ARCHITECTURE.md) for system design and data flow.

```
transcript/                  # CLI application
│
├── cmd/
│   └── transcript/
│       └── main.go             # Entry point, root command, exit codes
│
├── internal/
│   ├── apierr/                 # Shared API error sentinels and retry logic
│   │   ├── errors.go           # ErrRateLimit, ErrQuotaExceeded, ErrTimeout, ErrAuthFailed, ErrBadRequest
│   │   ├── errors_test.go
│   │   ├── retry.go            # RetryConfig + RetryWithBackoff[T]
│   │   └── retry_test.go
│   │
│   ├── audio/                  # Audio recording and chunking
│   │   ├── chunker.go          # SilenceChunker - split at pauses
│   │   ├── chunker_test.go
│   │   ├── deps.go             # External dependency interfaces
│   │   ├── errors.go           # Sentinel errors
│   │   ├── loopback.go         # System audio capture (BlackHole, PulseAudio)
│   │   ├── loopback_test.go
│   │   ├── recorder.go         # FFmpegRecorder - microphone/mix recording
│   │   └── recorder_test.go
│   │
│   ├── cli/                    # CLI commands and environment
│   │   ├── config.go           # `config` command (get/set/list)
│   │   ├── config_test.go
│   │   ├── env.go              # Env struct, factories, dependency injection
│   │   ├── env_test.go
│   │   ├── errors.go           # CLI-specific sentinel errors
│   │   ├── errors_test.go
│   │   ├── helpers_test.go     # Shared test helpers
│   │   ├── live.go             # `live` command (record + transcribe)
│   │   ├── live_test.go
│   │   ├── mocks_test.go       # Test mocks for factories
│   │   ├── output.go           # Shared output helpers (writeOutput, etc.)
│   │   ├── output_test.go
│   │   ├── provider.go         # Provider type (validated LLM provider)
│   │   ├── provider_test.go
│   │   ├── record.go           # `record` command
│   │   ├── record_test.go
│   │   ├── restructure.go      # Shared restructuring logic
│   │   ├── restructure_test.go
│   │   ├── structure.go        # `structure` command
│   │   ├── structure_test.go
│   │   ├── transcribe.go       # `transcribe` command
│   │   └── transcribe_test.go
│   │
│   ├── config/                 # User configuration
│   │   ├── config.go           # Load/Save, path resolution
│   │   └── config_test.go
│   │
│   ├── ffmpeg/                 # FFmpeg binary management
│   │   ├── deps.go             # External dependency interfaces
│   │   ├── errors.go           # Sentinel errors
│   │   ├── exec.go             # Command execution
│   │   ├── exec_test.go
│   │   ├── resolve.go          # Auto-download, PATH resolution
│   │   └── resolve_test.go
│   │
│   ├── format/                 # Output formatting utilities
│   │   ├── format.go           # DurationHuman(), Size()
│   │   └── format_test.go
│   │
│   ├── interrupt/              # Graceful interrupt handling
│   │   ├── handler.go          # Double Ctrl+C detection
│   │   └── handler_test.go
│   │
│   ├── lang/                   # Language validation
│   │   ├── errors.go           # Sentinel errors
│   │   ├── language.go         # ISO 639-1 validation
│   │   └── language_test.go
│   │
│   ├── restructure/            # Transcript restructuring (LLM, direct HTTP)
│   │   ├── deepseek.go         # DeepSeek provider (direct HTTP)
│   │   ├── deepseek_test.go
│   │   ├── errors.go           # Domain-specific errors (ErrTranscriptTooLong, ErrEmptyAPIKey)
│   │   ├── export_test.go      # Export internals for testing
│   │   ├── mapreduce.go        # MapReduceRestructurer for long texts
│   │   ├── openai.go           # OpenAI provider (direct HTTP)
│   │   ├── openai_test.go
│   │   ├── restructure.go      # Restructurer interface + estimateTokens
│   │   └── restructurer_test.go
│   │
│   ├── template/               # Restructuring templates
│   │   ├── template.go         # brainstorm, meeting, lecture, notes
│   │   └── template_test.go
│   │
│   └── transcribe/             # Audio transcription (direct HTTP, no external SDK)
│       ├── export_test.go      # Export internals for testing
│       ├── transcriber.go      # OpenAITranscriber, parallel execution
│       └── transcriber_test.go
│
├── docs/                       # Documentation
│   ├── ARCHITECTURE.md         # System design
│   └── LAYOUT.md               # This file
│
├── scripts/
│   └── setup-labels.sh         # GitHub labels setup
│
├── .github/
│   ├── ISSUE_TEMPLATE/         # Issue templates
│   │   ├── bug_report.yml
│   │   ├── config.yml
│   │   └── feature_request.yml
│   └── workflows/
│       ├── ci.yml              # CI pipeline
│       └── release.yml         # GoReleaser
│
├── .env.example                # Environment template
├── .gitignore
├── .goreleaser.yml             # Release configuration
├── codecov.yml                 # Coverage settings
├── CONTRIBUTING.md             # Contribution guidelines
├── go.mod                      # Module definition
├── go.sum                      # Dependency checksums
├── LICENSE                     # BSD-3-Clause
├── Makefile                    # Build, test, lint commands
└── README.md                   # User documentation
```

## Package Responsibilities

| Package              | Purpose                                      |
| -------------------- | -------------------------------------------- |
| `cmd/transcript`     | Entry point, root command, signal handling   |
| `internal/apierr`    | Shared API error sentinels, retry with backoff |
| `internal/cli`       | Cobra commands, dependency injection         |
| `internal/audio`     | FFmpeg recording, silence-based chunking     |
| `internal/transcribe`| OpenAI transcription via direct HTTP, parallel processing |
| `internal/restructure`| LLM-based formatting via direct HTTP (DeepSeek, OpenAI) |
| `internal/template`  | Prompt templates for restructuring           |
| `internal/config`    | User settings (~/.config/transcript/)     |
| `internal/ffmpeg`    | Binary resolution, auto-download             |
| `internal/format`    | Human-readable formatting utilities          |
| `internal/interrupt` | Graceful shutdown, double Ctrl+C detection   |
| `internal/lang`      | ISO 639-1 language code validation           |

## Conventions

- **CLI at cmd/** - Single binary entry point
- **internal/** - All business logic (not importable externally)
- **Flat packages** - Avoid deep nesting
- **Factory pattern** - Dependency injection via `Env`
- **Sentinel errors** - Use `errors.Is()` for type checking

## Test Conventions

| Pattern              | Purpose                        | Example                  |
| -------------------- | ------------------------------ | ------------------------ |
| `*_test.go`          | Unit tests (same package)      | `chunker_test.go`        |
| `mocks_test.go`      | Shared test mocks              | `internal/cli/mocks_test.go` |
| `export_test.go`     | Export internals for testing   | `internal/cli/export_test.go` |

## Makefile Targets

Run `make help` to see all available commands with descriptions.

### Build & Run

| Target        | Description                              |
| ------------- | ---------------------------------------- |
| `make build`  | Build the binary with version injection  |
| `make run`    | Build and run the binary                 |
| `make clean`  | Remove build artifacts and temp files    |
| `make version`| Show version that would be injected      |

### Testing

| Target              | Description                              | Requirements           |
| ------------------- | ---------------------------------------- | ---------------------- |
| `make test`         | Run unit tests                           | -                      |
| `make test-integration` | Run integration tests                | FFmpeg                 |
| `make test-e2e`     | Run E2E tests                            | FFmpeg + API keys      |
| `make test-all`     | Run all tests (unit + integration + e2e) | FFmpeg + API keys      |
| `make test-cover`   | Run unit tests with HTML coverage report | -                      |
| `make bench`        | Run benchmarks                           | -                      |

### Code Quality

| Target        | Description                              |
| ------------- | ---------------------------------------- |
| `make fmt`    | Format source code                       |
| `make vet`    | Run go vet for static analysis           |
| `make lint`   | Run staticcheck linter                   |
| `make sec`    | Run gosec security scanner               |
| `make check`  | Run all checks (fmt, vet, lint, test)    |
| `make check-all` | Full CI checks including integration  |

### Setup

| Target        | Description                              |
| ------------- | ---------------------------------------- |
| `make tools`  | Install staticcheck and gosec            |
| `make deps`   | Install dependencies                     |

### Development Helpers

| Target                    | Description                              |
| ------------------------- | ---------------------------------------- |
| `make record-test`        | Record a 10s test audio                  |
| `make transcribe-test`    | Transcribe test.ogg                      |
| `make live-test`          | Full live test (30s + transcription)     |
| `make testdata`           | Regenerate test audio fixtures           |

## CLI Commands

| Command     | File                          | Purpose                        |
| ----------- | ----------------------------- | ------------------------------ |
| `record`    | `internal/cli/record.go`      | Audio recording                |
| `transcribe`| `internal/cli/transcribe.go`  | File transcription             |
| `live`      | `internal/cli/live.go`        | Record + transcribe            |
| `structure` | `internal/cli/structure.go`   | Re-restructure existing transcript |
| `config`    | `internal/cli/config.go`      | Configuration management       |

## Environment Variables

| Variable              | Package            | Purpose                        |
| --------------------- | ------------------ | ------------------------------ |
| `OPENAI_API_KEY`      | `internal/cli`     | Transcription API key          |
| `DEEPSEEK_API_KEY`    | `internal/cli`     | Restructuring API key          |
| `TRANSCRIPT_OUTPUT_DIR`| `internal/config` | Default output directory       |
| `FFMPEG_PATH`         | `internal/ffmpeg`  | Custom FFmpeg binary           |
| `XDG_CONFIG_HOME`     | `internal/config`  | Config directory override      |

## Restructuring Templates

| Template    | File                          | Output Style                   |
| ----------- | ----------------------------- | ------------------------------ |
| `brainstorm`| `internal/template/template.go`| Ideas grouped by theme        |
| `meeting`   | `internal/template/template.go`| Decisions, actions, topics    |
| `lecture`   | `internal/template/template.go`| Readable prose                |
| `notes`     | `internal/template/template.go`| Hierarchical bullet points    |

## Supported Audio Formats

| Format | Extension | Notes                          |
| ------ | --------- | ------------------------------ |
| Ogg    | `.ogg`    | Recording output format        |
| MP3    | `.mp3`    | OpenAI accepts                 |
| WAV    | `.wav`    | OpenAI accepts                 |
| M4A    | `.m4a`    | OpenAI accepts                 |
| FLAC   | `.flac`   | OpenAI accepts                 |
| MP4    | `.mp4`    | OpenAI accepts                 |
| WEBM   | `.webm`   | OpenAI accepts                 |
