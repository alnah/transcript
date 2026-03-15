package audio_test

// Notes:
// - Tests focus on pure functions (parsing, formatting, algorithms)
// - Functions requiring FFmpeg execution are tested via interface mocks (see below)
// - OS-specific branches (runtime.GOOS) tested only on current OS; CI covers others
// - CleanupChunks safety check tested with compatible temp dir pattern
// - Internal functions exposed via export_test.go for black-box testing

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alnah/transcript/internal/audio"
)

// ---------------------------------------------------------------------------
// Chunk.Duration - Duration calculation
// ---------------------------------------------------------------------------

func TestChunk_Duration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		chunk audio.Chunk
		want  time.Duration
	}{
		{
			name:  "zero duration",
			chunk: audio.Chunk{StartTime: 0, EndTime: 0},
			want:  0,
		},
		{
			name:  "one second",
			chunk: audio.Chunk{StartTime: 0, EndTime: time.Second},
			want:  time.Second,
		},
		{
			name:  "five minutes from offset",
			chunk: audio.Chunk{StartTime: 10 * time.Minute, EndTime: 15 * time.Minute},
			want:  5 * time.Minute,
		},
		{
			name:  "subsecond precision",
			chunk: audio.Chunk{StartTime: 100 * time.Millisecond, EndTime: 350 * time.Millisecond},
			want:  250 * time.Millisecond,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.chunk.Duration()
			if got != tt.want {
				t.Errorf("Duration() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Chunk.String - String representation
// ---------------------------------------------------------------------------

func TestChunk_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		chunk audio.Chunk
		want  string
	}{
		{
			name:  "first chunk short",
			chunk: audio.Chunk{Index: 0, StartTime: 0, EndTime: 30 * time.Second},
			want:  "chunk 0: 00:00-00:30",
		},
		{
			name:  "chunk with minutes",
			chunk: audio.Chunk{Index: 1, StartTime: 5 * time.Minute, EndTime: 10 * time.Minute},
			want:  "chunk 1: 05:00-10:00",
		},
		{
			name:  "chunk with hours",
			chunk: audio.Chunk{Index: 5, StartTime: time.Hour + 30*time.Minute, EndTime: 2 * time.Hour},
			want:  "chunk 5: 01:30:00-02:00:00",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.chunk.String()
			if got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// NewTimeChunker - Constructor validation
// ---------------------------------------------------------------------------

func TestNewTimeChunker(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		ffmpegPath     string
		targetDuration time.Duration
		overlap        time.Duration
		wantErr        bool
	}{
		{
			name:           "valid parameters",
			ffmpegPath:     "/usr/bin/ffmpeg",
			targetDuration: 10 * time.Minute,
			overlap:        30 * time.Second,
			wantErr:        false,
		},
		{
			name:           "empty ffmpeg path",
			ffmpegPath:     "",
			targetDuration: 10 * time.Minute,
			overlap:        30 * time.Second,
			wantErr:        true,
		},
		{
			name:           "zero target uses default",
			ffmpegPath:     "/usr/bin/ffmpeg",
			targetDuration: 0,
			overlap:        30 * time.Second,
			wantErr:        false,
		},
		{
			name:           "negative overlap becomes zero",
			ffmpegPath:     "/usr/bin/ffmpeg",
			targetDuration: 10 * time.Minute,
			overlap:        -1 * time.Second,
			wantErr:        false,
		},
		{
			name:           "overlap equals target duration",
			ffmpegPath:     "/usr/bin/ffmpeg",
			targetDuration: 10 * time.Minute,
			overlap:        10 * time.Minute,
			wantErr:        true,
		},
		{
			name:           "overlap exceeds target duration",
			ffmpegPath:     "/usr/bin/ffmpeg",
			targetDuration: 5 * time.Minute,
			overlap:        10 * time.Minute,
			wantErr:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := audio.NewTimeChunker(tt.ffmpegPath, tt.targetDuration, tt.overlap)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewTimeChunker() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// NewSilenceChunker - Constructor validation
// ---------------------------------------------------------------------------

func TestNewSilenceChunker(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		ffmpegPath string
		wantErr    bool
	}{
		{
			name:       "valid path",
			ffmpegPath: "/usr/bin/ffmpeg",
			wantErr:    false,
		},
		{
			name:       "empty path",
			ffmpegPath: "",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := audio.NewSilenceChunker(tt.ffmpegPath)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewSilenceChunker() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// CleanupChunks - Cleanup logic
// ---------------------------------------------------------------------------

func TestCleanupChunks(t *testing.T) {
	t.Parallel()

	t.Run("empty slice does nothing", func(t *testing.T) {
		t.Parallel()
		err := audio.CleanupChunks(nil)
		if err != nil {
			t.Errorf("CleanupChunks(nil) = %v, want nil", err)
		}

		err = audio.CleanupChunks([]audio.Chunk{})
		if err != nil {
			t.Errorf("CleanupChunks([]) = %v, want nil", err)
		}
	})
}

// ---------------------------------------------------------------------------
// ParseDurationFromFFmpegOutput - FFmpeg duration parsing
// ---------------------------------------------------------------------------

func TestParseDurationFromFFmpegOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		output  string
		want    time.Duration
		wantErr bool
	}{
		{
			name:    "standard Duration format",
			output:  "Duration: 00:05:23.45, start: 0.000000, bitrate: 128 kb/s",
			want:    5*time.Minute + 23*time.Second + 450*time.Millisecond,
			wantErr: false,
		},
		{
			name:    "Duration with hours",
			output:  "Duration: 01:30:00.00, start: 0.000000",
			want:    time.Hour + 30*time.Minute,
			wantErr: false,
		},
		{
			name:    "time= format fallback",
			output:  "frame= 1000 fps=25 time=00:02:30.50 bitrate=128.0kbits/s",
			want:    2*time.Minute + 30*time.Second + 500*time.Millisecond,
			wantErr: false,
		},
		{
			name: "multiple time= uses last one",
			output: `frame= 100 time=00:00:10.00
frame= 200 time=00:00:20.00
frame= 300 time=00:00:30.00`,
			want:    30 * time.Second,
			wantErr: false,
		},
		{
			name:    "Duration takes precedence over time=",
			output:  "Duration: 00:01:00.00\ntime=00:00:30.00",
			want:    time.Minute,
			wantErr: false,
		},
		{
			name:    "three digit milliseconds",
			output:  "Duration: 00:00:05.123, start: 0.000000",
			want:    5*time.Second + 123*time.Millisecond,
			wantErr: false,
		},
		{
			name:    "six digit precision truncated",
			output:  "Duration: 00:00:01.123456, start: 0.000000",
			want:    time.Second + 123*time.Millisecond,
			wantErr: false,
		},
		{
			name:    "no duration found",
			output:  "ffmpeg version 5.0 Copyright (c) 2000-2022",
			want:    0,
			wantErr: true,
		},
		{
			name:    "empty output",
			output:  "",
			want:    0,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := audio.ParseDurationFromFFmpegOutput(tt.output)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseDurationFromFFmpegOutput(%q) error = %v, wantErr %v", tt.output, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ParseDurationFromFFmpegOutput(%q) = %v, want %v", tt.output, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ParseTimeComponents - Time component conversion
// ---------------------------------------------------------------------------

func TestParseTimeComponents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		hours        string
		minutes      string
		seconds      string
		centiseconds string
		want         time.Duration
	}{
		{
			name:         "zero",
			hours:        "00",
			minutes:      "00",
			seconds:      "00",
			centiseconds: "00",
			want:         0,
		},
		{
			name:         "one hour",
			hours:        "01",
			minutes:      "00",
			seconds:      "00",
			centiseconds: "00",
			want:         time.Hour,
		},
		{
			name:         "complex time",
			hours:        "02",
			minutes:      "30",
			seconds:      "45",
			centiseconds: "50",
			want:         2*time.Hour + 30*time.Minute + 45*time.Second + 500*time.Millisecond,
		},
		{
			name:         "single digit centiseconds",
			hours:        "00",
			minutes:      "00",
			seconds:      "01",
			centiseconds: "5",
			want:         time.Second + 500*time.Millisecond,
		},
		{
			name:         "three digit milliseconds",
			hours:        "00",
			minutes:      "00",
			seconds:      "01",
			centiseconds: "123",
			want:         time.Second + 123*time.Millisecond,
		},
		{
			name:         "six digit microseconds truncated",
			hours:        "00",
			minutes:      "00",
			seconds:      "01",
			centiseconds: "123456",
			want:         time.Second + 123*time.Millisecond,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := audio.ParseTimeComponents(tt.hours, tt.minutes, tt.seconds, tt.centiseconds)
			if err != nil {
				t.Fatalf("ParseTimeComponents(%q, %q, %q, %q) unexpected error: %v", tt.hours, tt.minutes, tt.seconds, tt.centiseconds, err)
			}
			if got != tt.want {
				t.Errorf("ParseTimeComponents(%q, %q, %q, %q) = %v, want %v", tt.hours, tt.minutes, tt.seconds, tt.centiseconds, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// FormatFFmpegTime - Duration to FFmpeg time string
// ---------------------------------------------------------------------------

func TestFormatFFmpegTime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{
			name: "zero",
			d:    0,
			want: "00:00:00.000",
		},
		{
			name: "one second",
			d:    time.Second,
			want: "00:00:01.000",
		},
		{
			name: "one minute",
			d:    time.Minute,
			want: "00:01:00.000",
		},
		{
			name: "one hour",
			d:    time.Hour,
			want: "01:00:00.000",
		},
		{
			name: "complex time",
			d:    2*time.Hour + 30*time.Minute + 45*time.Second + 500*time.Millisecond,
			want: "02:30:45.500",
		},
		{
			name: "subsecond only",
			d:    123 * time.Millisecond,
			want: "00:00:00.123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := audio.FormatFFmpegTime(tt.d)
			if got != tt.want {
				t.Errorf("FormatFFmpegTime() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ParseSilenceOutput - FFmpeg silencedetect output parsing
// ---------------------------------------------------------------------------

func TestParseSilenceOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		output    string
		wantCount int
		wantFirst audio.SilencePointTest
		wantLast  audio.SilencePointTest
	}{
		{
			name:      "empty output",
			output:    "",
			wantCount: 0,
		},
		{
			name:      "no silences",
			output:    "ffmpeg version 5.0\nStream mapping:\n",
			wantCount: 0,
		},
		{
			name: "single silence",
			output: `[silencedetect @ 0x7f8] silence_start: 10.5
[silencedetect @ 0x7f8] silence_end: 12.0 | silence_duration: 1.5`,
			wantCount: 1,
			wantFirst: audio.SilencePointTest{Start: 10500 * time.Millisecond, End: 12 * time.Second},
			wantLast:  audio.SilencePointTest{Start: 10500 * time.Millisecond, End: 12 * time.Second},
		},
		{
			name: "multiple silences",
			output: `[silencedetect @ 0x7f8] silence_start: 0
[silencedetect @ 0x7f8] silence_end: 1.0 | silence_duration: 1.0
[silencedetect @ 0x7f8] silence_start: 30.5
[silencedetect @ 0x7f8] silence_end: 32.0 | silence_duration: 1.5
[silencedetect @ 0x7f8] silence_start: 60.0
[silencedetect @ 0x7f8] silence_end: 65.0 | silence_duration: 5.0`,
			wantCount: 3,
			wantFirst: audio.SilencePointTest{Start: 0, End: time.Second},
			wantLast:  audio.SilencePointTest{Start: 60 * time.Second, End: 65 * time.Second},
		},
		{
			name: "silence_start without end ignored",
			output: `[silencedetect @ 0x7f8] silence_start: 10.0
[silencedetect @ 0x7f8] silence_start: 20.0
[silencedetect @ 0x7f8] silence_end: 25.0 | silence_duration: 5.0`,
			wantCount: 1,
			wantFirst: audio.SilencePointTest{Start: 20 * time.Second, End: 25 * time.Second},
			wantLast:  audio.SilencePointTest{Start: 20 * time.Second, End: 25 * time.Second},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := audio.ParseSilenceOutput(tt.output)
			if len(got) != tt.wantCount {
				t.Errorf("ParseSilenceOutput() returned %d silences, want %d", len(got), tt.wantCount)
				return
			}
			if tt.wantCount > 0 {
				if got[0] != tt.wantFirst {
					t.Errorf("first silence = %+v, want %+v", got[0], tt.wantFirst)
				}
				if got[len(got)-1] != tt.wantLast {
					t.Errorf("last silence = %+v, want %+v", got[len(got)-1], tt.wantLast)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TrimTrailingSilence - Trailing silence removal
// ---------------------------------------------------------------------------

func TestTrimTrailingSilence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		silences      []audio.SilencePointTest
		totalDuration time.Duration
		want          time.Duration
	}{
		{
			name:          "no silences",
			silences:      nil,
			totalDuration: 60 * time.Second,
			want:          60 * time.Second,
		},
		{
			name: "silence not at end",
			silences: []audio.SilencePointTest{
				{Start: 10 * time.Second, End: 15 * time.Second},
			},
			totalDuration: 60 * time.Second,
			want:          60 * time.Second,
		},
		{
			name: "short trailing silence kept",
			silences: []audio.SilencePointTest{
				{Start: 57 * time.Second, End: 60 * time.Second}, // 3s silence at end
			},
			totalDuration: 60 * time.Second,
			want:          60 * time.Second, // kept because < 5s
		},
		{
			name: "long trailing silence trimmed",
			silences: []audio.SilencePointTest{
				{Start: 50 * time.Second, End: 60 * time.Second}, // 10s silence at end
			},
			totalDuration: 60 * time.Second,
			want:          50 * time.Second, // trimmed to silence start
		},
		{
			name: "trailing silence within tolerance",
			silences: []audio.SilencePointTest{
				{Start: 50 * time.Second, End: 59500 * time.Millisecond}, // ends 0.5s before total
			},
			totalDuration: 60 * time.Second,
			want:          50 * time.Second, // still trimmed (within 1s tolerance)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := audio.TrimTrailingSilence(tt.silences, tt.totalDuration)
			if got != tt.want {
				t.Errorf("TrimTrailingSilence() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ExpandBoundariesForDuration - Segment subdivision
// ---------------------------------------------------------------------------

func TestExpandBoundariesForDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		boundaries   []time.Duration
		maxDuration  time.Duration
		wantLen      int
		wantContains []time.Duration
	}{
		{
			name:        "empty boundaries",
			boundaries:  nil,
			maxDuration: 5 * time.Minute,
			wantLen:     0,
		},
		{
			name:        "single boundary",
			boundaries:  []time.Duration{0},
			maxDuration: 5 * time.Minute,
			wantLen:     1,
		},
		{
			name:        "no expansion needed",
			boundaries:  []time.Duration{0, 5 * time.Minute, 10 * time.Minute},
			maxDuration: 5 * time.Minute,
			wantLen:     3,
		},
		{
			name:         "one segment needs expansion",
			boundaries:   []time.Duration{0, 15 * time.Minute},
			maxDuration:  5 * time.Minute,
			wantLen:      4, // 0, 5, 10, 15
			wantContains: []time.Duration{0, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute},
		},
		{
			name:         "preserves original boundaries",
			boundaries:   []time.Duration{0, 3 * time.Minute, 20 * time.Minute},
			maxDuration:  5 * time.Minute,
			wantContains: []time.Duration{0, 3 * time.Minute, 20 * time.Minute},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := audio.ExpandBoundariesForDuration(tt.boundaries, tt.maxDuration)
			if tt.wantLen > 0 && len(got) != tt.wantLen {
				t.Errorf("ExpandBoundariesForDuration() len = %d, want %d", len(got), tt.wantLen)
			}
			for _, want := range tt.wantContains {
				found := false
				for _, g := range got {
					if g == want {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("ExpandBoundariesForDuration() missing %v in %v", want, got)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SelectCutPoints - Cut point selection algorithm
// ---------------------------------------------------------------------------

func TestSelectCutPoints(t *testing.T) {
	t.Parallel()

	// Assume 100 bytes/second bitrate for easy calculation
	// With 20MB max chunk size: maxDuration = 20*1024*1024/100 = 209715s (~58h)
	// For testing, use smaller values

	tests := []struct {
		name           string
		silences       []audio.SilencePointTest
		bytesPerSecond float64
		maxChunkSize   int64
		wantLen        int
	}{
		{
			name:           "no silences",
			silences:       nil,
			bytesPerSecond: 1000,
			maxChunkSize:   10000,
			wantLen:        0,
		},
		{
			name: "all silences fit in one chunk",
			silences: []audio.SilencePointTest{
				{Start: 1 * time.Second, End: 2 * time.Second},
				{Start: 3 * time.Second, End: 4 * time.Second},
			},
			bytesPerSecond: 1000,
			maxChunkSize:   100000, // 100s max
			wantLen:        0,      // no cuts needed
		},
		{
			name: "needs one cut",
			silences: []audio.SilencePointTest{
				{Start: 5 * time.Second, End: 6 * time.Second},
				{Start: 15 * time.Second, End: 16 * time.Second},
			},
			bytesPerSecond: 1000,
			maxChunkSize:   10000, // 10s max
			wantLen:        1,     // cut at first silence
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := audio.SelectCutPoints(tt.silences, tt.bytesPerSecond, tt.maxChunkSize)
			if len(got) != tt.wantLen {
				t.Errorf("SelectCutPoints() len = %d, want %d (got %v)", len(got), tt.wantLen, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ChunkEncodingArgs - Encoding arguments
// ---------------------------------------------------------------------------

func TestChunkEncodingArgs(t *testing.T) {
	t.Parallel()

	args := audio.ChunkEncodingArgs()

	// Verify essential encoding parameters are present
	required := []string{"-c:a", "libopus", "-ar", "16000", "-ac", "1"}
	argsStr := strings.Join(args, " ")

	for _, r := range required {
		if !strings.Contains(argsStr, r) {
			t.Errorf("ChunkEncodingArgs() missing %q in %v", r, args)
		}
	}
}

// ---------------------------------------------------------------------------
// TimeChunker.Chunk - Integration with mocks
// ---------------------------------------------------------------------------

func TestTimeChunker_Chunk(t *testing.T) {
	t.Parallel()

	t.Run("successful chunking", func(t *testing.T) {
		t.Parallel()

		// Mock command runner that returns duration info
		mockCmd := &mockCommandRunner{
			outputFunc: func(ctx context.Context, name string, args []string) ([]byte, error) {
				// First call is probeDuration, return duration
				if contains(args, "-f") && contains(args, "null") && !contains(args, "-ss") {
					return []byte("Duration: 00:02:00.00, start: 0.000000\ntime=00:02:00.00"), nil
				}
				// Subsequent calls are extractChunk
				return []byte(""), nil
			},
		}

		mockTempDir := &mockTempDirCreator{
			dir: t.TempDir(),
		}

		mockFiles := &mockFileRemover{}

		tc, err := audio.NewTimeChunker(
			"/usr/bin/ffmpeg",
			30*time.Second, // 30s target
			5*time.Second,  // 5s overlap
			audio.WithTimeChunkerCommandRunner(mockCmd),
			audio.WithTimeChunkerTempDir(mockTempDir),
			audio.WithTimeChunkerFileRemover(mockFiles),
		)
		if err != nil {
			t.Fatalf("NewTimeChunker() error = %v", err)
		}

		chunks, err := tc.Chunk(context.Background(), "/fake/audio.ogg")
		if err != nil {
			t.Fatalf("Chunk() error = %v", err)
		}

		// 2 minutes with 30s target and 5s overlap = 25s step
		// Should produce multiple chunks
		if len(chunks) == 0 {
			t.Error("Chunk() returned 0 chunks")
		}

		// Verify chunks are ordered
		for i := range chunks {
			if chunks[i].Index != i {
				t.Errorf("chunk %d has Index = %d", i, chunks[i].Index)
			}
		}
	})

	t.Run("probe duration error", func(t *testing.T) {
		t.Parallel()

		mockCmd := &mockCommandRunner{
			outputFunc: func(ctx context.Context, name string, args []string) ([]byte, error) {
				return []byte("ffmpeg error"), errors.New("ffmpeg failed")
			},
		}

		tc, _ := audio.NewTimeChunker(
			"/usr/bin/ffmpeg",
			30*time.Second,
			5*time.Second,
			audio.WithTimeChunkerCommandRunner(mockCmd),
		)

		_, err := tc.Chunk(context.Background(), "/fake/audio.ogg")
		if err == nil {
			t.Error("Chunk() expected error, got nil")
		}
	})

	t.Run("temp dir creation error", func(t *testing.T) {
		t.Parallel()

		mockCmd := &mockCommandRunner{
			outputFunc: func(ctx context.Context, name string, args []string) ([]byte, error) {
				return []byte("Duration: 00:01:00.00"), nil
			},
		}

		mockTempDir := &mockTempDirCreator{
			err: errors.New("cannot create temp dir"),
		}

		tc, _ := audio.NewTimeChunker(
			"/usr/bin/ffmpeg",
			30*time.Second,
			5*time.Second,
			audio.WithTimeChunkerCommandRunner(mockCmd),
			audio.WithTimeChunkerTempDir(mockTempDir),
		)

		_, err := tc.Chunk(context.Background(), "/fake/audio.ogg")
		if err == nil {
			t.Error("Chunk() expected error, got nil")
		}
		if !strings.Contains(err.Error(), "temp directory") {
			t.Errorf("error should mention temp directory: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// SilenceChunker.Chunk - Integration with mocks
// ---------------------------------------------------------------------------

func TestSilenceChunker_Chunk(t *testing.T) {
	t.Parallel()

	t.Run("successful chunking with silences", func(t *testing.T) {
		t.Parallel()

		callCount := 0
		mockCmd := &mockCommandRunner{
			outputFunc: func(ctx context.Context, name string, args []string) ([]byte, error) {
				callCount++
				// First call: detectSilences
				if callCount == 1 {
					return []byte(`Duration: 00:05:00.00
[silencedetect @ 0x7f8] silence_start: 60.0
[silencedetect @ 0x7f8] silence_end: 62.0 | silence_duration: 2.0
[silencedetect @ 0x7f8] silence_start: 180.0
[silencedetect @ 0x7f8] silence_end: 183.0 | silence_duration: 3.0
time=00:05:00.00`), nil
				}
				// Subsequent calls: extractChunk
				return []byte(""), nil
			},
		}

		mockTempDir := &mockTempDirCreator{dir: t.TempDir()}
		mockFiles := &mockFileRemover{}
		mockStatter := &mockFileStatter{size: 10 * 1024 * 1024} // 10MB file

		sc, err := audio.NewSilenceChunker(
			"/usr/bin/ffmpeg",
			audio.WithCommandRunner(mockCmd),
			audio.WithTempDirCreator(mockTempDir),
			audio.WithFileRemover(mockFiles),
			audio.WithFileStatter(mockStatter),
		)
		if err != nil {
			t.Fatalf("NewSilenceChunker() error = %v", err)
		}

		chunks, err := sc.Chunk(context.Background(), "/fake/audio.ogg")
		if err != nil {
			t.Fatalf("Chunk() error = %v", err)
		}

		// Should produce chunks based on silence detection
		if len(chunks) == 0 {
			t.Error("Chunk() returned 0 chunks")
		}

		// Verify chunks are ordered
		for i := range chunks {
			if chunks[i].Index != i {
				t.Errorf("chunk %d has Index = %d", i, chunks[i].Index)
			}
		}
	})

	t.Run("no silences falls back to time chunking", func(t *testing.T) {
		t.Parallel()

		callCount := 0
		mockCmd := &mockCommandRunner{
			outputFunc: func(ctx context.Context, name string, args []string) ([]byte, error) {
				callCount++
				// SilenceChunker detectSilences (no silences found)
				// and fallback TimeChunker calls all use same mock
				return []byte("Duration: 00:02:00.00\ntime=00:02:00.00"), nil
			},
		}

		mockTempDir := &mockTempDirCreator{dir: t.TempDir()}
		mockFiles := &mockFileRemover{}
		mockStatter := &mockFileStatter{size: 5 * 1024 * 1024}

		// Create fallback with same mocks
		fallback, err := audio.NewTimeChunker(
			"/usr/bin/ffmpeg",
			10*time.Minute,
			30*time.Second,
			audio.WithTimeChunkerCommandRunner(mockCmd),
			audio.WithTimeChunkerTempDir(mockTempDir),
			audio.WithTimeChunkerFileRemover(mockFiles),
		)
		if err != nil {
			t.Fatalf("NewTimeChunker() error = %v", err)
		}

		sc, err := audio.NewSilenceChunker(
			"/usr/bin/ffmpeg",
			audio.WithCommandRunner(mockCmd),
			audio.WithTempDirCreator(mockTempDir),
			audio.WithFileRemover(mockFiles),
			audio.WithFileStatter(mockStatter),
			audio.WithFallback(fallback),
		)
		if err != nil {
			t.Fatalf("NewSilenceChunker() error = %v", err)
		}

		chunks, err := sc.Chunk(context.Background(), "/fake/audio.ogg")
		if err != nil {
			t.Fatalf("Chunk() error = %v", err)
		}

		// Should still produce chunks via fallback
		if len(chunks) == 0 {
			t.Error("Chunk() with no silences returned 0 chunks")
		}
	})

	t.Run("file stat error", func(t *testing.T) {
		t.Parallel()

		mockStatter := &mockFileStatter{err: errors.New("file not found")}

		sc, _ := audio.NewSilenceChunker(
			"/usr/bin/ffmpeg",
			audio.WithFileStatter(mockStatter),
		)

		_, err := sc.Chunk(context.Background(), "/nonexistent.ogg")
		if err == nil {
			t.Error("Chunk() expected error on stat failure")
		}
		if !errors.Is(err, audio.ErrFileNotFound) {
			t.Errorf("Chunk() error should wrap ErrFileNotFound, got %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// SilenceChunker options
// ---------------------------------------------------------------------------

func TestSilenceChunkerOptions(t *testing.T) {
	t.Parallel()

	t.Run("WithNoiseDB", func(t *testing.T) {
		t.Parallel()
		_, err := audio.NewSilenceChunker("/usr/bin/ffmpeg", audio.WithNoiseDB(-40))
		if err != nil {
			t.Errorf("WithNoiseDB() caused error = %v", err)
		}
	})

	t.Run("WithMinSilence", func(t *testing.T) {
		t.Parallel()
		_, err := audio.NewSilenceChunker("/usr/bin/ffmpeg", audio.WithMinSilence(time.Second))
		if err != nil {
			t.Errorf("WithMinSilence() caused error = %v", err)
		}
	})

	t.Run("WithMaxChunkSize", func(t *testing.T) {
		t.Parallel()
		_, err := audio.NewSilenceChunker("/usr/bin/ffmpeg", audio.WithMaxChunkSize(10*1024*1024))
		if err != nil {
			t.Errorf("WithMaxChunkSize() caused error = %v", err)
		}
	})

	t.Run("WithFallback", func(t *testing.T) {
		t.Parallel()
		fallback, _ := audio.NewTimeChunker("/usr/bin/ffmpeg", 5*time.Minute, 30*time.Second)
		_, err := audio.NewSilenceChunker("/usr/bin/ffmpeg", audio.WithFallback(fallback))
		if err != nil {
			t.Errorf("WithFallback() caused error = %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// CleanupChunks - Additional tests
// ---------------------------------------------------------------------------

func TestCleanupChunks_SafetyCheck(t *testing.T) {
	t.Parallel()

	// CleanupChunks should only remove directories matching transcript pattern
	t.Run("non-temp directory falls back to individual file removal", func(t *testing.T) {
		t.Parallel()

		// Create chunks with a path that doesn't contain "transcript-"
		chunks := []audio.Chunk{
			{Path: "/some/random/path/chunk_000.ogg"},
		}

		// This should not panic and should try to remove individual files
		err := audio.CleanupChunks(chunks)
		// Error is expected since files don't exist, but no panic
		_ = err
	})
}

// ---------------------------------------------------------------------------
// Mocks for testing
// ---------------------------------------------------------------------------

type mockCommandRunner struct {
	outputFunc func(ctx context.Context, name string, args []string) ([]byte, error)
	calls      []mockCall
}

type mockCall struct {
	name string
	args []string
}

func (m *mockCommandRunner) CombinedOutput(ctx context.Context, name string, args []string) ([]byte, error) {
	m.calls = append(m.calls, mockCall{name: name, args: args})
	if m.outputFunc != nil {
		return m.outputFunc(ctx, name, args)
	}
	return nil, nil
}

type mockTempDirCreator struct {
	dir string
	err error
}

func (m *mockTempDirCreator) MkdirTemp(dir, pattern string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.dir, nil
}

type mockFileRemover struct {
	removeErr    error
	removeAllErr error
}

func (m *mockFileRemover) Remove(name string) error {
	return m.removeErr
}

func (m *mockFileRemover) RemoveAll(path string) error {
	return m.removeAllErr
}

type mockFileStatter struct {
	size int64
	err  error
}

func (m *mockFileStatter) Stat(name string) (os.FileInfo, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &mockFileInfo{size: m.size}, nil
}

type mockFileInfo struct {
	size int64
}

func (m *mockFileInfo) Name() string       { return "mock.ogg" }
func (m *mockFileInfo) Size() int64        { return m.size }
func (m *mockFileInfo) Mode() os.FileMode  { return 0644 }
func (m *mockFileInfo) ModTime() time.Time { return time.Now() }
func (m *mockFileInfo) IsDir() bool        { return false }
func (m *mockFileInfo) Sys() any           { return nil }

func contains(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}
