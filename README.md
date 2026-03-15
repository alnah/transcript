# transcript

[![Go Reference](https://img.shields.io/badge/go.dev-reference-007d9c?logo=go&logoColor=white)](https://pkg.go.dev/github.com/alnah/transcript)
[![Go Report Card](https://img.shields.io/badge/go%20report-A+-brightgreen)](https://goreportcard.com/report/github.com/alnah/transcript)
[![Build Status](https://img.shields.io/github/actions/workflow/status/alnah/transcript/ci.yml?branch=main)](https://github.com/alnah/transcript/actions)
[![Coverage](https://img.shields.io/codecov/c/github/alnah/transcript)](https://codecov.io/gh/alnah/transcript)
[![License](https://img.shields.io/badge/License-BSD--3--Clause-blue.svg)](LICENSE)

> Record, transcribe, and restructure audio via CLI - microphone/loopback capture, automatic chunking, parallel transcription, and template-based formatting.

## Table of Contents

- [Installation](#installation)
- [Quick Start](#quick-start)
- [Features](#features)
- [How it Works](#how-it-works)
- [CLI Reference](#cli-reference)
- [Environment Variables](#environment-variables)
- [Configuration](#configuration)
- [Templates](#templates)
  - [Pricing](#pricing)
  - [Best Practices](#best-practices)
- [Troubleshooting](#troubleshooting)
- [Known Limitations](#known-limitations)
- [Contributing](#contributing)

## Installation

```bash
go install github.com/alnah/transcript/cmd/transcript@latest
```

<details>
<summary>Other installation methods</summary>

### Build from Source

```bash
git clone https://github.com/alnah/transcript.git
cd transcript
make build
```

### Binary Download

Download pre-built binaries from [GitHub Releases](https://github.com/alnah/transcript/releases).

</details>

## Requirements

- Go 1.25+
- FFmpeg (downloaded automatically on first run)
- OpenAI API key

> **Note:** FFmpeg is auto-downloaded for macOS (arm64/amd64), Linux (amd64), and Windows (amd64). Set `FFMPEG_PATH` to use a custom binary.

### Quick Setup

Create a `.env` file in your working directory (auto-loaded on startup):

```bash
# Copy the example file
cp .env.example .env
# Then edit with your API keys
```

The `.env.example` file contains:

```bash
OPENAI_API_KEY=sk-your-key-here      # Required for transcription
DEEPSEEK_API_KEY=sk-your-key-here    # Required for restructuring (default provider)
```

Or export directly:

```bash
export OPENAI_API_KEY=sk-...
export DEEPSEEK_API_KEY=sk-...  # Only needed if using --template
```

## Quick Start

```bash
# Set your API keys
export OPENAI_API_KEY=sk-...
export DEEPSEEK_API_KEY=sk-...  # For restructuring

# Record and transcribe a meeting
transcript live -d 1h -o meeting.md -t meeting

# Transcribe an existing recording
transcript transcribe recording.ogg -o notes.md -t brainstorm

# Record system audio (video call)
transcript record -d 30m -s -o call.ogg

# Restructure an existing transcript
transcript structure raw_notes.md -t lecture -o lecture.md
```

## Features

- **Audio recording** - Microphone, system audio (loopback), or both mixed
- **Automatic chunking** - Splits at silences to respect OpenAI's 25MB limit
- **Parallel transcription** - Concurrent API requests (configurable 1-10)
- **Template restructuring** - `brainstorm`, `meeting`, `lecture`, `notes` formats
- **Multi-provider support** - OpenAI or DeepSeek for restructuring
- **Language support** - Specify audio language, translate output
- **Graceful interrupts** - Ctrl+C stops recording, continues transcription

## How it Works

```
┌─────────┐    ┌─────────┐    ┌────────────┐    ┌─────────────┐    ┌────────┐
│  Audio  │───▶│ FFmpeg  │───▶│  Chunking  │───▶│ Transcribe  │───▶│ Output │
│  Input  │    │ Record  │    │ (silences) │    │  (OpenAI)   │    │  .md   │
└─────────┘    └─────────┘    └────────────┘    └─────────────┘    └────────┘
     │                                                │                  │
     │                                                ▼                  │
     │                                        ┌─────────────┐            │
     │                                        │ Restructure │────────────┘
     │                                        │ (DeepSeek/  │
     │                                        │   OpenAI)   │
     │                                        └─────────────┘
     │
     ├── Microphone (default)
     ├── System audio (-s/--system-record)
     └── Both mixed (--mix)
```

1. **Record**: Capture audio via FFmpeg (mic, system audio, or mixed)
2. **Chunk**: Split at natural silences to respect OpenAI's 25MB limit
3. **Transcribe**: Parallel API calls to OpenAI (`gpt-4o-mini-transcribe`)
4. **Restructure** (optional): Format with template via DeepSeek or OpenAI

## CLI Reference

```bash
transcript <command> [flags]

Commands:
  record       Record audio to file
  transcribe   Transcribe audio file to text
  live         Record and transcribe in one step
  structure    Restructure an existing transcript
  config       Manage configuration
  help         Help about any command
  version      Show version information
```

### record

Record audio from microphone, system audio, or both.

```bash
transcript record -d 2h -o session.ogg      # Microphone
transcript record -d 30m -s -o system.ogg   # System audio
transcript record -d 1h --mix -o meeting.ogg # Both mixed
```

<details>
<summary>All flags</summary>

| Flag              | Short | Default                     | Description                                |
|-------------------|-------|-----------------------------|--------------------------------------------|
| `--duration`      | `-d`  | required                    | Recording duration (e.g., `30s`, `5m`, `2h`) |
| `--output`        | `-o`  | `recording_<timestamp>.ogg` | Output file path                           |
| `--device`        |       | system default              | Specific audio input device                |
| `--system-record` | `-s`  | `false`                     | Capture system audio instead of microphone |
| `--mix`           |       | `false`                     | Capture both microphone and system audio   |

`--system-record` and `--mix` are mutually exclusive.

</details>

### transcribe

Transcribe an existing audio file.

```bash
transcript transcribe audio.ogg -o notes.md
transcript transcribe lecture.mp3 -o notes.md -t lecture
transcript transcribe french.ogg -o notes.md -l fr -T en -t meeting
```

<details>
<summary>All flags</summary>

| Flag          | Short | Default       | Description                                                      |
|---------------|-------|---------------|------------------------------------------------------------------|
| `--output`    | `-o`  | `<input>.md`  | Output file path                                                 |
| `--template`  | `-t`  |               | Restructure template: `brainstorm`, `meeting`, `lecture`, `notes`|
| `--provider`  |       | `deepseek`    | LLM provider for restructuring: `deepseek`, `openai`             |
| `--language`  | `-l`  | auto-detect   | Audio language (ISO 639-1: `en`, `fr`, `pt-BR`)                  |
| `--translate` | `-T`  | same as input | Translate output to language (requires `--template`)             |
| `--parallel`  | `-p`  | `10`          | Max concurrent API requests (1-10)                               |
| `--diarize`   |       | `false`       | Enable speaker identification                                    |

`--translate` requires `--template`.

</details>

### live

Record and transcribe in one step. Press Ctrl+C to stop recording early and continue with transcription. Press Ctrl+C twice within 2 seconds to abort entirely.

```bash
transcript live -d 30m -o notes.md
transcript live -d 1h -o meeting.md -t meeting -k        # Keep audio
transcript live -d 1h -s -t meeting                      # System audio
transcript live -d 1h -t meeting -K                      # Keep audio + raw transcript
```

<details>
<summary>All flags</summary>

Inherits all flags from `record` and `transcribe`, plus:

| Flag                   | Short | Default | Description                                                      |
|------------------------|-------|---------|------------------------------------------------------------------|
| `--keep-audio`         | `-k`  | `false` | Preserve the audio file after transcription                      |
| `--keep-raw-transcript`| `-r`  | `false` | Keep raw transcript before restructuring (requires `--template`) |
| `--keep-all`           | `-K`  | `false` | Keep both audio and raw transcript (equivalent to `-k -r`)       |

</details>

### structure

Restructure an existing transcript file using a template. Useful for re-processing raw transcripts generated without `--template`.

```bash
transcript structure meeting_raw.md -t meeting -o meeting.md
transcript structure notes.md -t brainstorm
transcript structure lecture.md -t lecture -T fr    # Translate to French
transcript structure raw.md -t notes --provider openai
```

<details>
<summary>All flags</summary>

| Flag          | Short | Default                 | Description                                                       |
|---------------|-------|-------------------------|-------------------------------------------------------------------|
| `--output`    | `-o`  | `<input>_structured.md` | Output file path                                                  |
| `--template`  | `-t`  | required                | Restructure template: `brainstorm`, `meeting`, `lecture`, `notes` |
| `--provider`  |       | `deepseek`              | LLM provider for restructuring: `deepseek`, `openai`              |
| `--translate` | `-T`  | same as input           | Translate output to language (ISO 639-1: `en`, `fr`)              |

</details>

### config

Manage persistent configuration.

```bash
transcript config set output-dir ~/Documents/transcripts
transcript config get output-dir
transcript config list
```

<details>
<summary>Exit codes</summary>

| Code | Name          | Description                                          |
|------|---------------|------------------------------------------------------|
| 0    | Success       | Operation completed successfully                     |
| 1    | General       | Unexpected or unclassified error                     |
| 2    | Usage         | Invalid flags or arguments                           |
| 3    | Setup         | FFmpeg not found, API key missing, no audio device   |
| 4    | Validation    | Unsupported format, file not found, invalid language |
| 5    | Transcription | Rate limit, quota exceeded, auth failed              |
| 6    | Restructure   | Transcript exceeds token limit                       |
| 130  | Interrupt     | Aborted via Ctrl+C                                   |

</details>

## Environment Variables

**Priority:** CLI flags > environment variables > config file > defaults

| Variable                | Required | Default | Description                                                              |
|-------------------------|----------|---------|--------------------------------------------------------------------------|
| `OPENAI_API_KEY`        | Yes      |         | OpenAI API key for transcription (and restructuring with `--provider openai`) |
| `DEEPSEEK_API_KEY`      | No       |         | DeepSeek API key (required when using `--template` with default provider)|
| `TRANSCRIPT_OUTPUT_DIR` | No       | `.`     | Default output directory                                                 |
| `FFMPEG_PATH`           | No       | auto    | Path to FFmpeg binary (skips auto-download)                              |

> **Tip:** Place a `.env` file in your working directory with these variables. It will be auto-loaded on startup via [godotenv](https://github.com/joho/godotenv). See `.env.example` for reference.

## Configuration

Config files are stored in the user config directory:

| OS      | Config Directory             |
|---------|------------------------------|
| Linux   | `~/.config/transcript/`   |
| macOS   | `~/.config/transcript/`   |
| Windows | `%APPDATA%\transcript\`   |

Respects `XDG_CONFIG_HOME` if set.

| Key          | Description                      |
|--------------|----------------------------------|
| `output-dir` | Default directory for output files |

<details>
<summary>Example config file</summary>

```ini
# ~/.config/transcript/config
output-dir=/Users/john/Documents/transcripts
```

</details>

## Templates

Templates transform raw transcripts into structured markdown.

| Template     | Purpose                    | Output Structure                                              |
|--------------|----------------------------|---------------------------------------------------------------|
| `brainstorm` | Idea generation sessions   | H1 topic, H2 themes, bullet points, key insights, actions     |
| `meeting`    | Meeting notes              | H1 subject, participants, topics discussed, decisions, action items |
| `lecture`    | Course/conference lectures | Readable prose with H1/H2/H3 headers, bold key terms          |
| `notes`      | Bullet-point lecture notes | H2 thematic headers, hierarchical bullet points, bold terms   |

Templates output English by default. Use `--translate` / `-T` to translate:

```bash
transcript transcribe audio.ogg -t meeting -T fr
```

### Provider Selection

Restructuring uses **DeepSeek** (`deepseek-reasoner`) by default because it delivers excellent results at a fraction of the cost. Use OpenAI (`o4-mini`) for faster processing:

```bash
# Default: DeepSeek (slower, cheaper, excellent quality)
transcript transcribe audio.ogg -t lecture

# OpenAI (faster, more expensive)
transcript transcribe audio.ogg -t lecture --provider openai
```

### Pricing

| Model                    | Input (per 1M tokens) | Output (per 1M tokens) | Notes                                  |
|--------------------------|-----------------------|------------------------|----------------------------------------|
| `gpt-4o-mini-transcribe` | $2.50                 | $10.00                 | Transcription                          |
| `o4-mini`                | $1.10                 | $4.40                  | OpenAI restructuring (100K max output) |
| `deepseek-reasoner`      | $0.21                 | $0.32                  | DeepSeek restructuring (64K max output)|

**Cost estimates** (assuming ~150 words/minute, ~200 tokens/minute):

| Operation                     | 1 hour recording | Cost estimate |
|-------------------------------|------------------|---------------|
| Transcription only            | ~12K tokens      | ~$0.15        |
| Transcription + restructuring (DeepSeek) | ~12K + ~15K tokens | ~$0.16  |
| Transcription + restructuring (OpenAI)   | ~12K + ~15K tokens | ~$0.22  |

DeepSeek is **~10x cheaper** for restructuring with comparable quality. It's slower (can take several minutes for long transcripts), but the cost savings are significant for heavy usage.

### Best Practices

Use `-K` (or `--keep-all`) to preserve intermediate files:

```bash
# Keep both audio and raw transcript for re-processing
transcript live -d 1h -t meeting -K -o meeting.md
```

This produces three files:
- `meeting.md` - the restructured output
- `meeting.ogg` - the audio recording (from `-k`)
- `meeting_raw.md` - the raw transcript before restructuring (from `-r`)

This allows you to:
- **Re-transcribe** if the initial transcription quality is poor
- **Re-restructure** with a different template without re-transcribing
- **Try multiple templates** on the same transcript (e.g., `lecture` vs `notes`)
- **Switch providers** to compare DeepSeek vs OpenAI results

```bash
# Re-restructure an existing transcript with a different template
transcript structure meeting_raw.md -t notes -o meeting_notes.md

# Try OpenAI instead of DeepSeek
transcript structure meeting_raw.md -t meeting --provider openai -o meeting_openai.md
```

## Supported Formats

OpenAI accepts: `ogg`, `mp3`, `wav`, `m4a`, `flac`, `mp4`, `mpeg`, `mpga`, `webm`

Recording output is always OGG Vorbis (16kHz mono, ~50kbps) optimized for voice.

## Troubleshooting

### FFmpeg not found

FFmpeg is auto-downloaded on first run. If download fails:

```bash
# macOS
brew install ffmpeg

# Ubuntu/Debian
sudo apt install ffmpeg

# Windows
winget install ffmpeg
```

Or set `FFMPEG_PATH` to your binary location.

### Loopback device not found

System audio capture requires a virtual audio driver:

<details>
<summary>macOS - BlackHole</summary>

```bash
brew install --cask blackhole-2ch
```

**Important:** BlackHole is a "black hole" - audio sent to it is NOT audible. To hear audio while recording:

1. Open "Audio MIDI Setup" (Spotlight search)
2. Click "+" > "Create Multi-Output Device"
3. Check both your speakers AND BlackHole 2ch
4. Set this Multi-Output as your system output

</details>

<details>
<summary>Linux - PulseAudio/PipeWire</summary>

Usually pre-installed. Loopback uses the monitor device of your default sink.

```bash
# Verify PulseAudio is working
pactl get-default-sink

# Install if missing
sudo apt install pulseaudio pulseaudio-utils
```

</details>

<details>
<summary>Windows - Stereo Mix or VB-Cable</summary>

**Option 1 - Enable Stereo Mix (recommended):**

1. Right-click speaker icon > Sound settings > More sound settings
2. Recording tab > Right-click > Show Disabled Devices
3. Enable "Stereo Mix" if present

**Option 2 - Install VB-Audio Virtual Cable:**

Download from: https://vb-audio.com/Cable/

</details>

### API errors

| Error                       | Cause                    | Solution                               |
|-----------------------------|--------------------------|----------------------------------------|
| "OPENAI_API_KEY not set"    | Missing API key          | `export OPENAI_API_KEY=sk-...`         |
| "DEEPSEEK_API_KEY not set"  | Missing key for DeepSeek | `export DEEPSEEK_API_KEY=sk-...`       |
| "rate limit exceeded"       | Too many requests        | Reduce `--parallel` or wait            |
| "quota exceeded"            | Billing issue            | Check OpenAI/DeepSeek account billing  |
| "authentication failed"     | Invalid API key          | Verify your API key                    |

### Transcript too long

Output token limits depend on the restructuring provider:

| Provider           | Max output tokens |
|--------------------|-------------------|
| `o4-mini` (OpenAI) | 100,000           |
| `deepseek-reasoner`| 64,000            |

For very long recordings:
- Skip restructuring (no `--template`) and use `structure` command later
- Split the audio file manually
- Use shorter recording sessions
- Try `--provider openai` for higher token limit

## Known Limitations

### By Design

| Not Supported       | Why                                    |
|---------------------|----------------------------------------|
| Real-time streaming | Uses batch API, not Realtime API       |
| Offline mode        | Requires internet (cloud APIs only)    |
| Video input         | Audio extraction not implemented       |

### Platform Notes

| Issue                                   | Solution                   |
|-----------------------------------------|----------------------------|
| No loopback on Linux without PulseAudio | Install pulseaudio         |
| BlackHole mutes audio on macOS          | Create Multi-Output Device |
| Stereo Mix disabled on Windows          | Enable in Sound settings   |

## Contributing

This project is currently in active development and **not accepting external contributions**.

Feel free to:
- Open issues for bug reports
- Suggest features via issues
- Fork for personal use

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup, or [docs/](docs/) for architecture and project layout.

## License

See: [BSD-3-Clause](LICENSE).
