package audio

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/alnah/transcript/internal/ffmpeg"
	"github.com/alnah/transcript/internal/format"
)

// Compile-time interface implementation checks.
var (
	_ Chunker = (*TimeChunker)(nil)
	_ Chunker = (*SilenceChunker)(nil)
)

// Chunk represents a segment of audio extracted from a larger file.
// The caller is responsible for cleaning up chunk files after use.
type Chunk struct {
	Path      string        // Absolute path to the chunk file.
	Index     int           // Zero-based index for ordering.
	StartTime time.Duration // Start timestamp in the source audio.
	EndTime   time.Duration // End timestamp in the source audio.
}

// Duration returns the length of this chunk.
func (c Chunk) Duration() time.Duration {
	return c.EndTime - c.StartTime
}

// String returns a human-readable representation for logging.
func (c Chunk) String() string {
	return fmt.Sprintf("chunk %d: %s-%s",
		c.Index,
		format.Duration(c.StartTime),
		format.Duration(c.EndTime))
}

// Chunker splits an audio file into smaller chunks suitable for transcription.
type Chunker interface {
	// Chunk splits audioPath into multiple chunk files.
	// Returns chunks ordered by their position in the source audio.
	// The caller is responsible for cleaning up the returned chunk files.
	Chunk(ctx context.Context, audioPath string) ([]Chunk, error)
}

// Default chunking parameters.
const (
	// defaultNoiseDB is the silence detection threshold in dB.
	// -30dB is suitable for voice recordings with typical background noise.
	defaultNoiseDB = -30.0

	// defaultMinSilence is the minimum silence duration to detect.
	// 0.5s catches natural pauses in speech without over-splitting.
	defaultMinSilence = 500 * time.Millisecond

	// defaultMaxChunkSize is the target maximum chunk size in bytes.
	// OpenAI limit is 25MB; we use 20MB for VBR safety margin.
	defaultMaxChunkSize = 20 * 1024 * 1024

	// defaultMaxChunkDuration is the maximum duration per chunk.
	// Shorter chunks (5min) maximize parallelism and reduce OpenAI truncation risk.
	defaultMaxChunkDuration = 5 * time.Minute

	// defaultSilenceChunkerOverlap is the overlap for silence-based chunking.
	// Each chunk starts slightly before its boundary to capture words at edges.
	defaultSilenceChunkerOverlap = 2 * time.Second

	// defaultOverlap is the overlap duration for time-based chunking.
	// 30s ensures words at chunk boundaries are captured in at least one chunk.
	defaultOverlap = 30 * time.Second

	// defaultTargetDuration is the target chunk duration for time-based chunking.
	defaultTargetDuration = 10 * time.Minute

	// trailingSilenceEndPadding is added after trimming trailing silence
	// to ensure the last words are captured by transcription.
	trailingSilenceEndPadding = 5 * time.Second
)

// WarnFunc is a callback for warning messages during chunking.
// Set to nil to suppress warnings, or provide a custom handler.
type WarnFunc func(msg string)

// defaultWarnFunc writes warnings to stderr.
func defaultWarnFunc(msg string) {
	fmt.Fprintln(os.Stderr, msg)
}

// TimeChunker splits audio into fixed-duration chunks with overlap.
// This is the fallback strategy when silence detection fails or finds no silences.
type TimeChunker struct {
	ffmpegPath     string
	targetDuration time.Duration
	overlap        time.Duration

	// Injectable dependencies (defaults to OS implementations).
	cmd     commandRunner
	tempDir tempDirCreator
	files   fileRemover
}

// TimeChunkerOption configures a TimeChunker.
type TimeChunkerOption func(*TimeChunker)

// WithTimeChunkerCommandRunner sets the command runner for TimeChunker.
func WithTimeChunkerCommandRunner(r commandRunner) TimeChunkerOption {
	return func(tc *TimeChunker) {
		tc.cmd = r
	}
}

// WithTimeChunkerTempDir sets the temp directory creator for TimeChunker.
func WithTimeChunkerTempDir(t tempDirCreator) TimeChunkerOption {
	return func(tc *TimeChunker) {
		tc.tempDir = t
	}
}

// WithTimeChunkerFileRemover sets the file remover for TimeChunker.
func WithTimeChunkerFileRemover(f fileRemover) TimeChunkerOption {
	return func(tc *TimeChunker) {
		tc.files = f
	}
}

// NewTimeChunker creates a TimeChunker with the specified parameters.
func NewTimeChunker(ffmpegPath string, targetDuration, overlap time.Duration, opts ...TimeChunkerOption) (*TimeChunker, error) {
	if ffmpegPath == "" {
		return nil, fmt.Errorf("ffmpegPath cannot be empty: %w", ffmpeg.ErrNotFound)
	}
	if targetDuration <= 0 {
		targetDuration = defaultTargetDuration
	}
	if overlap < 0 {
		overlap = 0
	}
	if overlap >= targetDuration {
		return nil, fmt.Errorf("%w: overlap %v >= target %v", ErrInvalidOverlap, overlap, targetDuration)
	}

	tc := &TimeChunker{
		ffmpegPath:     ffmpegPath,
		targetDuration: targetDuration,
		overlap:        overlap,
		cmd:            osCommandRunner{},
		tempDir:        osTempDirCreator{},
		files:          osFileRemover{},
	}

	for _, opt := range opts {
		opt(tc)
	}

	return tc, nil
}

// Chunk splits the audio file into fixed-duration segments with overlap.
func (tc *TimeChunker) Chunk(ctx context.Context, audioPath string) ([]Chunk, error) {
	// Get total duration of the audio file.
	totalDuration, err := tc.probeDuration(ctx, audioPath)
	if err != nil {
		return nil, fmt.Errorf("failed to probe audio duration: %w", err)
	}

	// Create temp directory for chunks.
	tempDir, err := tc.tempDir.MkdirTemp("", "transcript-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp directory: %w", err)
	}

	// Calculate chunk boundaries.
	var chunks []Chunk
	step := tc.targetDuration - tc.overlap
	for i := 0; ; i++ {
		start := time.Duration(i) * step
		if start >= totalDuration {
			break
		}
		end := min(start+tc.targetDuration, totalDuration)

		chunkPath := filepath.Join(tempDir, fmt.Sprintf("chunk_%03d.ogg", i))
		if err := tc.extractChunk(ctx, audioPath, chunkPath, start, end); err != nil {
			_ = tc.files.RemoveAll(tempDir) // best-effort cleanup; original error takes precedence
			return nil, err
		}

		chunks = append(chunks, Chunk{
			Path:      chunkPath,
			Index:     i,
			StartTime: start,
			EndTime:   end,
		})

		// Last chunk reached the end.
		if end >= totalDuration {
			break
		}
	}

	return chunks, nil
}

// probeDuration returns the duration of an audio file using ffprobe/ffmpeg.
func (tc *TimeChunker) probeDuration(ctx context.Context, audioPath string) (time.Duration, error) {
	// Use ffmpeg to get duration (ffprobe may not be available).
	// The -i flag with no output shows file info including duration.
	args := []string{
		"-i", audioPath,
		"-f", "null", "-",
	}
	output, err := tc.cmd.CombinedOutput(ctx, tc.ffmpegPath, args)
	if err != nil {
		// FFmpeg returns non-zero even when it successfully reads file info,
		// so we try to parse the output anyway.
		if len(output) == 0 {
			return 0, err
		}
	}

	return parseDurationFromFFmpegOutput(string(output))
}

// parseDurationFromFFmpegOutput extracts duration from FFmpeg stderr.
// Looks for: "Duration: HH:MM:SS.ms" or "time=HH:MM:SS.ms"
func parseDurationFromFFmpegOutput(output string) (time.Duration, error) {
	// Pattern: Duration: 00:05:23.45
	durationRe := regexp.MustCompile(`Duration:\s*(\d+):(\d+):(\d+)\.(\d+)`)
	if matches := durationRe.FindStringSubmatch(output); matches != nil {
		return parseTimeComponents(matches[1], matches[2], matches[3], matches[4])
	}

	// Fallback pattern: time=00:05:23.45 (from progress output)
	timeRe := regexp.MustCompile(`time=(\d+):(\d+):(\d+)\.(\d+)`)
	// Find all matches and use the last one (final time).
	allMatches := timeRe.FindAllStringSubmatch(output, -1)
	if len(allMatches) > 0 {
		matches := allMatches[len(allMatches)-1]
		return parseTimeComponents(matches[1], matches[2], matches[3], matches[4])
	}

	return 0, fmt.Errorf("could not parse duration from ffmpeg output")
}

// parseTimeComponents converts HH:MM:SS.ms strings to Duration.
func parseTimeComponents(hours, minutes, seconds, fractional string) (time.Duration, error) {
	h, _ := strconv.Atoi(hours)
	m, _ := strconv.Atoi(minutes)
	s, _ := strconv.Atoi(seconds)

	// Normalize fractional part to milliseconds.
	// Input may be 1-6+ digits (e.g., ".4", ".45", ".456", ".456789").
	frac, _ := strconv.Atoi(fractional)
	ms := frac
	switch n := len(fractional); {
	case n == 1:
		ms = frac * 100
	case n == 2:
		ms = frac * 10
	case n == 3:
		// Already milliseconds.
	case n > 3:
		// Truncate excess precision by dividing.
		for i := n; i > 3; i-- {
			ms /= 10
		}
	}

	return time.Duration(h)*time.Hour +
		time.Duration(m)*time.Minute +
		time.Duration(s)*time.Second +
		time.Duration(ms)*time.Millisecond, nil
}

// chunkEncodingArgs returns FFmpeg encoding arguments for chunk extraction.
// Re-encodes to OGG Opus to ensure valid output even from corrupted/truncated sources.
// Uses same parameters as recording (16kHz mono, ~50kbps) optimal for speech transcription.
func chunkEncodingArgs() []string {
	return []string{
		"-c:a", "libopus",
		"-ar", "16000",
		"-ac", "1",
		"-b:a", "50k",
	}
}

// runExtractChunk extracts a segment from audioPath to chunkPath using FFmpeg.
// Re-encodes to OGG Opus to ensure valid output even from corrupted/truncated sources.
func runExtractChunk(ctx context.Context, cmd commandRunner, ffmpegPath, audioPath, chunkPath string, start, end time.Duration) error {
	args := []string{
		"-y",
		"-i", audioPath,
		"-ss", formatFFmpegTime(start),
		"-to", formatFFmpegTime(end),
	}
	args = append(args, chunkEncodingArgs()...)
	args = append(args, chunkPath)

	output, err := cmd.CombinedOutput(ctx, ffmpegPath, args)
	if err != nil {
		return fmt.Errorf("%w: failed to extract chunk %s: %v\nOutput: %s",
			ErrChunkingFailed, chunkPath, err, string(output))
	}
	return nil
}

// extractChunk extracts a segment from audioPath to chunkPath.
func (tc *TimeChunker) extractChunk(ctx context.Context, audioPath, chunkPath string, start, end time.Duration) error {
	return runExtractChunk(ctx, tc.cmd, tc.ffmpegPath, audioPath, chunkPath, start, end)
}

// formatFFmpegTime formats a duration for FFmpeg -ss/-to arguments.
func formatFFmpegTime(d time.Duration) string {
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := d.Seconds() - float64(h*3600+m*60)
	return fmt.Sprintf("%02d:%02d:%06.3f", h, m, s)
}

// SilenceChunker splits audio at detected silence points.
// Falls back to TimeChunker if no silences are found.
type SilenceChunker struct {
	ffmpegPath   string
	noiseDB      float64
	minSilence   time.Duration
	maxChunkSize int64
	fallback     Chunker
	warn         WarnFunc

	// Injectable dependencies (defaults to OS implementations).
	cmd     commandRunner
	tempDir tempDirCreator
	files   fileRemover
	statter fileStatter
}

// SilenceChunkerOption configures a SilenceChunker.
type SilenceChunkerOption func(*SilenceChunker)

// WithNoiseDB sets the silence detection threshold in dB.
// Lower values (more negative) detect quieter sounds as silence.
// Default: -30dB.
func WithNoiseDB(db float64) SilenceChunkerOption {
	return func(sc *SilenceChunker) {
		sc.noiseDB = db
	}
}

// WithMinSilence sets the minimum silence duration to detect.
// Default: 500ms.
func WithMinSilence(d time.Duration) SilenceChunkerOption {
	return func(sc *SilenceChunker) {
		sc.minSilence = d
	}
}

// WithMaxChunkSize sets the target maximum chunk size in bytes.
// Default: 20MB (with safety margin for OpenAI's 25MB limit).
func WithMaxChunkSize(size int64) SilenceChunkerOption {
	return func(sc *SilenceChunker) {
		sc.maxChunkSize = size
	}
}

// WithFallback sets a custom fallback Chunker.
// Default: TimeChunker with 10min target, 30s overlap.
func WithFallback(c Chunker) SilenceChunkerOption {
	return func(sc *SilenceChunker) {
		sc.fallback = c
	}
}

// WithCommandRunner sets the command runner for SilenceChunker.
func WithCommandRunner(r commandRunner) SilenceChunkerOption {
	return func(sc *SilenceChunker) {
		sc.cmd = r
	}
}

// WithTempDirCreator sets the temp directory creator for SilenceChunker.
func WithTempDirCreator(t tempDirCreator) SilenceChunkerOption {
	return func(sc *SilenceChunker) {
		sc.tempDir = t
	}
}

// WithFileRemover sets the file remover for SilenceChunker.
func WithFileRemover(f fileRemover) SilenceChunkerOption {
	return func(sc *SilenceChunker) {
		sc.files = f
	}
}

// WithFileStatter sets the file statter for SilenceChunker.
func WithFileStatter(s fileStatter) SilenceChunkerOption {
	return func(sc *SilenceChunker) {
		sc.statter = s
	}
}

// WithWarnFunc sets a callback for warning messages.
// By default, warnings are written to stderr. Set to nil to suppress.
func WithWarnFunc(fn WarnFunc) SilenceChunkerOption {
	return func(sc *SilenceChunker) {
		sc.warn = fn
	}
}

// NewSilenceChunker creates a SilenceChunker with functional options.
// If no fallback is provided, a default TimeChunker is created.
func NewSilenceChunker(ffmpegPath string, opts ...SilenceChunkerOption) (*SilenceChunker, error) {
	if ffmpegPath == "" {
		return nil, fmt.Errorf("ffmpegPath cannot be empty: %w", ffmpeg.ErrNotFound)
	}

	sc := &SilenceChunker{
		ffmpegPath:   ffmpegPath,
		noiseDB:      defaultNoiseDB,
		minSilence:   defaultMinSilence,
		maxChunkSize: defaultMaxChunkSize,
		warn:         defaultWarnFunc,
		cmd:          osCommandRunner{},
		tempDir:      osTempDirCreator{},
		files:        osFileRemover{},
		statter:      osFileStatter{},
	}

	for _, opt := range opts {
		opt(sc)
	}

	// Create default fallback if not provided.
	if sc.fallback == nil {
		fallback, err := NewTimeChunker(ffmpegPath, defaultTargetDuration, defaultOverlap)
		if err != nil {
			return nil, fmt.Errorf("failed to create fallback chunker: %w", err)
		}
		sc.fallback = fallback
	}

	return sc, nil
}

// Chunk splits the audio file at silence points.
// If no silences are found, falls back to time-based chunking.
func (sc *SilenceChunker) Chunk(ctx context.Context, audioPath string) ([]Chunk, error) {
	// Get file info for bitrate estimation.
	fileInfo, err := sc.statter.Stat(audioPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFileNotFound, err)
	}
	fileSize := fileInfo.Size()

	// Detect silences.
	silences, totalDuration, err := sc.detectSilences(ctx, audioPath)
	if err != nil {
		// Warn and fall back to time-based chunking.
		if sc.warn != nil {
			sc.warn(fmt.Sprintf("Warning: silence detection failed (%v), using time-based chunking", err))
		}
		return sc.fallback.Chunk(ctx, audioPath)
	}

	// No silences found - fall back to time-based chunking.
	if len(silences) == 0 {
		if sc.warn != nil {
			sc.warn("Warning: no silences detected, using time-based chunking (may cut mid-sentence)")
		}
		return sc.fallback.Chunk(ctx, audioPath)
	}

	// Trim trailing silence: if last silence extends to end of file, use its start as effective end.
	// This prevents OpenAI from truncating transcriptions when chunks end with long silence.
	effectiveDuration := trimTrailingSilence(silences, totalDuration)
	if effectiveDuration < totalDuration {
		effectiveDuration = min(effectiveDuration+trailingSilenceEndPadding, totalDuration)
	}

	// Calculate average bitrate for size estimation (use effective duration for accuracy).
	avgBitrate := float64(fileSize) / totalDuration.Seconds() // bytes per second

	// Select cut points that keep chunks under maxChunkSize.
	cutPoints := sc.selectCutPoints(silences, avgBitrate)

	// Create temp directory for chunks.
	tempDir, err := sc.tempDir.MkdirTemp("", "transcript-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp directory: %w", err)
	}

	// Extract chunks using effective duration (excluding trailing silence).
	chunks, err := sc.extractChunks(ctx, audioPath, tempDir, cutPoints, effectiveDuration)
	if err != nil {
		_ = sc.files.RemoveAll(tempDir) // best-effort cleanup; original error takes precedence
		return nil, err
	}

	return chunks, nil
}

// trimTrailingSilence returns an effective end duration excluding trailing silence.
// If the last silence extends to (or very close to) the end of the file, we use
// the start of that silence as the effective end. This prevents OpenAI from
// truncating transcriptions when audio ends with long silence.
func trimTrailingSilence(silences []silencePoint, totalDuration time.Duration) time.Duration {
	if len(silences) == 0 {
		return totalDuration
	}

	lastSilence := silences[len(silences)-1]

	// Check if last silence extends to the end of the file (within 1 second tolerance).
	// Trim trailing silence >= 5 seconds to avoid OpenAI truncation issues.
	const tolerance = 1 * time.Second
	const minTrailingSilence = 5 * time.Second

	silenceDuration := lastSilence.end - lastSilence.start
	extendsToEnd := totalDuration-lastSilence.end < tolerance

	if extendsToEnd && silenceDuration >= minTrailingSilence {
		return lastSilence.start
	}

	return totalDuration
}

// silencePoint represents a detected silence in the audio.
type silencePoint struct {
	start time.Duration
	end   time.Duration
}

// midpoint returns the middle of the silence, ideal for cutting.
func (s silencePoint) midpoint() time.Duration {
	return s.start + (s.end-s.start)/2
}

// detectSilences runs FFmpeg silencedetect and parses the output.
// Returns silence points and total audio duration.
func (sc *SilenceChunker) detectSilences(ctx context.Context, audioPath string) ([]silencePoint, time.Duration, error) {
	args := []string{
		"-i", audioPath,
		"-af", fmt.Sprintf("silencedetect=noise=%ddB:d=%.2f",
			int(sc.noiseDB),
			sc.minSilence.Seconds()),
		"-f", "null",
		"-",
	}

	output, err := sc.cmd.CombinedOutput(ctx, sc.ffmpegPath, args)
	if err != nil {
		// FFmpeg may return non-zero even on success, try parsing output
		if len(output) == 0 {
			return nil, 0, err
		}
	}

	outputStr := string(output)
	silences := parseSilenceOutput(outputStr)
	duration, err := parseDurationFromFFmpegOutput(outputStr)
	if err != nil {
		return nil, 0, fmt.Errorf("could not determine audio duration: %w", err)
	}

	return silences, duration, nil
}

// parseSilenceOutput extracts silence points from FFmpeg silencedetect output.
// FFmpeg outputs lines like:
//
//	[silencedetect @ 0x...] silence_start: 42.123
//	[silencedetect @ 0x...] silence_end: 43.456 | silence_duration: 1.333
func parseSilenceOutput(output string) []silencePoint {
	var silences []silencePoint
	var currentStart time.Duration
	hasStart := false

	// Regex patterns - tolerant of format variations.
	startRe := regexp.MustCompile(`silence_start:\s*([\d.]+)`)
	endRe := regexp.MustCompile(`silence_end:\s*([\d.]+)`)

	for line := range strings.SplitSeq(output, "\n") {
		if matches := startRe.FindStringSubmatch(line); matches != nil {
			seconds, err := strconv.ParseFloat(matches[1], 64)
			if err == nil {
				currentStart = time.Duration(seconds * float64(time.Second))
				hasStart = true
			}
		}
		if matches := endRe.FindStringSubmatch(line); matches != nil && hasStart {
			seconds, err := strconv.ParseFloat(matches[1], 64)
			if err == nil {
				silences = append(silences, silencePoint{
					start: currentStart,
					end:   time.Duration(seconds * float64(time.Second)),
				})
				hasStart = false
			}
		}
	}

	return silences
}

// selectCutPoints chooses silence midpoints that keep chunks under maxChunkSize.
// Uses a greedy algorithm: accumulate silences as candidates until the next
// silence would exceed maxDuration, then cut at the last valid candidate.
func (sc *SilenceChunker) selectCutPoints(silences []silencePoint, bytesPerSecond float64) []time.Duration {
	if len(silences) == 0 {
		return nil
	}

	// Calculate max duration per chunk based on size limit.
	maxDuration := time.Duration(float64(sc.maxChunkSize) / bytesPerSecond * float64(time.Second))

	var cutPoints []time.Duration
	lastCut := time.Duration(0)
	var candidate *time.Duration // Last valid cut point before exceeding maxDuration

	for _, silence := range silences {
		mid := silence.midpoint()
		durationSinceCut := mid - lastCut

		if durationSinceCut < maxDuration {
			// This silence is a valid candidate (chunk would be under limit).
			candidate = &mid
		} else {
			// We've exceeded max duration.
			if candidate != nil {
				// Cut at the last valid candidate.
				cutPoints = append(cutPoints, *candidate)
				lastCut = *candidate
				candidate = nil
				// Re-evaluate current silence from new lastCut.
				if mid-lastCut < maxDuration {
					candidate = &mid
				}
			} else {
				// No valid candidate available, must cut here even though over limit.
				cutPoints = append(cutPoints, mid)
				lastCut = mid
			}
		}
	}

	return cutPoints
}

// extractChunks creates chunk files at the specified cut points.
// If extraction fails partway through, already-created chunk files are cleaned up.
// Segments exceeding defaultMaxChunkDuration are automatically subdivided.
// Each chunk (except the first) starts with a small overlap to capture words at boundaries.
func (sc *SilenceChunker) extractChunks(ctx context.Context, audioPath, tempDir string, cutPoints []time.Duration, totalDuration time.Duration) ([]Chunk, error) {
	// Build segment boundaries: [0, cut1, cut2, ..., totalDuration].
	boundaries := make([]time.Duration, 0, len(cutPoints)+2)
	boundaries = append(boundaries, 0)
	boundaries = append(boundaries, cutPoints...)
	boundaries = append(boundaries, totalDuration)

	// Expand boundaries to ensure no segment exceeds maxChunkDuration.
	// This handles cases where silence detection finds few/no silences.
	boundaries = expandBoundariesForDuration(boundaries, defaultMaxChunkDuration)

	chunks := make([]Chunk, 0, len(boundaries)-1)
	for i := range len(boundaries) - 1 {
		start := boundaries[i]
		end := boundaries[i+1]

		// Apply overlap: start each chunk (except first) slightly earlier.
		// This ensures words at boundaries are captured in at least one chunk.
		extractStart := start
		if i > 0 && start >= defaultSilenceChunkerOverlap {
			extractStart = start - defaultSilenceChunkerOverlap
		}

		chunkPath := filepath.Join(tempDir, fmt.Sprintf("chunk_%03d.ogg", i))
		if err := sc.extractChunk(ctx, audioPath, chunkPath, extractStart, end); err != nil {
			for _, c := range chunks {
				_ = sc.files.Remove(c.Path) // best-effort cleanup; original error takes precedence
			}
			return nil, err
		}

		chunks = append(chunks, Chunk{
			Path:      chunkPath,
			Index:     i,
			StartTime: start, // Logical start (for ordering), not extract start
			EndTime:   end,
		})
	}

	return chunks, nil
}

// expandBoundariesForDuration subdivides segments that exceed maxDuration.
// Maintains original boundaries and adds intermediate points as needed.
func expandBoundariesForDuration(boundaries []time.Duration, maxDuration time.Duration) []time.Duration {
	if len(boundaries) < 2 {
		return boundaries
	}

	expanded := make([]time.Duration, 0, len(boundaries))
	for i := 0; i < len(boundaries)-1; i++ {
		start := boundaries[i]
		end := boundaries[i+1]
		expanded = append(expanded, start)

		// If segment exceeds max duration, subdivide it
		segmentDuration := end - start
		if segmentDuration > maxDuration {
			// Calculate number of sub-segments needed
			numSegments := int((segmentDuration + maxDuration - 1) / maxDuration) // ceiling division
			subDuration := segmentDuration / time.Duration(numSegments)

			// Add intermediate boundaries
			for j := 1; j < numSegments; j++ {
				expanded = append(expanded, start+subDuration*time.Duration(j))
			}
		}
	}
	// Add final boundary
	expanded = append(expanded, boundaries[len(boundaries)-1])

	return expanded
}

// extractChunk extracts a segment from audioPath to chunkPath.
// Re-encodes to OGG Opus to ensure valid output even from corrupted/truncated sources.
func (sc *SilenceChunker) extractChunk(ctx context.Context, audioPath, chunkPath string, start, end time.Duration) error {
	return runExtractChunk(ctx, sc.cmd, sc.ffmpegPath, audioPath, chunkPath, start, end)
}

// CleanupChunks removes all chunk files and their parent directory.
// Call this after transcription is complete.
func CleanupChunks(chunks []Chunk) error {
	if len(chunks) == 0 {
		return nil
	}

	// All chunks should be in the same temp directory.
	tempDir := filepath.Dir(chunks[0].Path)

	// Verify it's a temp directory before removing.
	if !strings.Contains(tempDir, "transcript-") {
		// Safety check: don't delete arbitrary directories.
		// Fall back to removing individual files.
		for _, chunk := range chunks {
			_ = os.Remove(chunk.Path) // best-effort cleanup; files may already be gone
		}
		return nil
	}

	return os.RemoveAll(tempDir)
}
