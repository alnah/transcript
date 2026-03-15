#!/bin/bash
# setup-labels.sh - Configure GitHub labels for transcript

set -e

# Type
gh label create "bug" --color "d73a4a" --description "Something isn't working" --force
gh label create "enhancement" --color "a2eeef" --description "New feature or request" --force
gh label create "documentation" --color "0075ca" --description "Improvements or additions to documentation" --force
gh label create "question" --color "d876e3" --description "Further information is requested" --force

# Area
gh label create "area/recording" --color "c5def5" --description "Audio recording (mic, loopback, mix)" --force
gh label create "area/transcription" --color "c5def5" --description "OpenAI transcription API" --force
gh label create "area/restructure" --color "c5def5" --description "Template-based restructuring" --force
gh label create "area/ffmpeg" --color "c5def5" --description "FFmpeg integration or auto-download" --force
gh label create "area/cli" --color "c5def5" --description "Command-line interface, flags, config" --force

# OS
gh label create "os/macos" --color "bfdadc" --description "macOS specific" --force
gh label create "os/linux" --color "bfdadc" --description "Linux specific" --force
gh label create "os/windows" --color "bfdadc" --description "Windows specific" --force

# Status
gh label create "needs-info" --color "fbca04" --description "Waiting for more information from reporter" --force
gh label create "confirmed" --color "0e8a16" --description "Bug reproduced or feature accepted" --force
gh label create "wontfix" --color "ffffff" --description "This will not be worked on" --force
gh label create "duplicate" --color "cfd3d7" --description "This issue already exists" --force
gh label create "known-limitation" --color "e4e669" --description "Documented in Known Limitations" --force

# Priority
gh label create "priority/high" --color "b60205" --description "Critical issue" --force
gh label create "priority/low" --color "c2e0c6" --description "Nice to have" --force

echo "Labels configured successfully"
