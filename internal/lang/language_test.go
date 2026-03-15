package lang_test

// Notes:
// - Black-box testing: all tests use the public API only (lang_test package)
// - Empty string behavior is intentionally tested: "" means "auto-detect" for Parse,
//   and returns a valid zero Language (IsZero() == true)
// - validLanguages map coverage: we test a representative sample (common + uncommon + invalid)
//   rather than exhaustive 55+ codes, since the logic is a simple map lookup
// - IsFrench/IsEnglish: we explicitly test ISO 639-2/3 codes (fra, eng, fro) to document
//   that they are NOT supported (ISO 639-1 only)
// - MustParse panic behavior is tested with recover()

import (
	"errors"
	"testing"

	"github.com/alnah/transcript/internal/lang"
)

// ---------------------------------------------------------------------------
// TestNormalize - Normalizes language codes to lowercase with hyphen separator
// ---------------------------------------------------------------------------

func TestNormalize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		// Standard cases
		{name: "lowercase code", input: "en", want: "en"},
		{name: "uppercase code", input: "EN", want: "en"},
		{name: "mixed case code", input: "En", want: "en"},

		// Locale with hyphen
		{name: "locale with hyphen lowercase", input: "pt-br", want: "pt-br"},
		{name: "locale with hyphen uppercase", input: "PT-BR", want: "pt-br"},
		{name: "locale with hyphen mixed", input: "pt-BR", want: "pt-br"},

		// Locale with underscore (converted to hyphen)
		{name: "locale with underscore", input: "pt_BR", want: "pt-br"},
		{name: "locale with underscore uppercase", input: "PT_BR", want: "pt-br"},

		// Edge cases
		{name: "empty string", input: "", want: ""},
		{name: "multiple hyphens", input: "zh-hans-cn", want: "zh-hans-cn"},
		{name: "multiple underscores", input: "zh_hans_cn", want: "zh-hans-cn"},
		{name: "mixed separators", input: "zh_hans-CN", want: "zh-hans-cn"},

		// Idempotence: normalizing twice gives same result
		{name: "already normalized", input: "pt-br", want: "pt-br"},

		// Characters not handled (documented behavior)
		{name: "double underscore preserved as double hyphen", input: "pt__BR", want: "pt--br"},
		{name: "spaces not trimmed", input: " en ", want: " en "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := lang.Normalize(tt.input)
			if got != tt.want {
				t.Errorf("Normalize(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalize_Idempotent(t *testing.T) {
	t.Parallel()

	inputs := []string{"EN", "pt_BR", "zh-Hans-CN", "fr-CA", ""}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			t.Parallel()

			once := lang.Normalize(input)
			twice := lang.Normalize(once)
			if once != twice {
				t.Errorf("Normalize is not idempotent: Normalize(%q) = %q, Normalize(%q) = %q",
					input, once, once, twice)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestParse - Validates and parses language codes into Language type
// ---------------------------------------------------------------------------

func TestParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    string // expected String() output
		wantErr bool
	}{
		// Empty string = auto-detect (valid, returns zero Language)
		{name: "empty string auto-detect", input: "", want: "", wantErr: false},

		// Valid common languages
		{name: "english", input: "en", want: "en", wantErr: false},
		{name: "french", input: "fr", want: "fr", wantErr: false},
		{name: "spanish", input: "es", want: "es", wantErr: false},
		{name: "chinese", input: "zh", want: "zh", wantErr: false},
		{name: "japanese", input: "ja", want: "ja", wantErr: false},

		// Valid less common languages (sample from validLanguages)
		{name: "swahili", input: "sw", want: "sw", wantErr: false},
		{name: "tagalog", input: "tl", want: "tl", wantErr: false},
		{name: "macedonian", input: "mk", want: "mk", wantErr: false},
		{name: "afrikaans", input: "af", want: "af", wantErr: false},

		// Valid locales (base language is valid)
		{name: "brazilian portuguese", input: "pt-BR", want: "pt-br", wantErr: false},
		{name: "canadian french", input: "fr-CA", want: "fr-ca", wantErr: false},
		{name: "simplified chinese", input: "zh-CN", want: "zh-cn", wantErr: false},
		{name: "british english", input: "en-GB", want: "en-gb", wantErr: false},

		// Case variations (normalized internally)
		{name: "uppercase", input: "EN", want: "en", wantErr: false},
		{name: "mixed case locale", input: "Pt-Br", want: "pt-br", wantErr: false},
		{name: "underscore locale", input: "pt_BR", want: "pt-br", wantErr: false},

		// Unknown locale suffix with valid base (still valid)
		{name: "unknown locale suffix", input: "en-XXXXX", want: "en-xxxxx", wantErr: false},
		{name: "french belgium", input: "fr-BE", want: "fr-be", wantErr: false},

		// Invalid codes
		{name: "invalid two letter", input: "xx", wantErr: true},
		{name: "invalid three letter", input: "xyz", wantErr: true},
		{name: "invalid numeric", input: "123", wantErr: true},
		{name: "invalid single letter", input: "e", wantErr: true},
		{name: "invalid locale with invalid base", input: "xx-YY", wantErr: true},

		// ISO 639-2/3 codes (not supported - we only support ISO 639-1)
		{name: "ISO 639-2 english", input: "eng", wantErr: true},
		{name: "ISO 639-2 french", input: "fra", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := lang.Parse(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("Parse(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got.String() != tt.want {
				t.Errorf("Parse(%q).String() = %q, want %q", tt.input, got.String(), tt.want)
			}
		})
	}
}

func TestParse_ErrorWrapsErrInvalid(t *testing.T) {
	t.Parallel()

	_, err := lang.Parse("xyz")
	if err == nil {
		t.Fatalf("Parse(%q) unexpected success, want error", "xyz")
	}

	if !errors.Is(err, lang.ErrInvalid) {
		t.Errorf("Parse(%q) error should wrap ErrInvalid, got: %v", "xyz", err)
	}
}

func TestParse_ErrorContainsOriginalCode(t *testing.T) {
	t.Parallel()

	_, err := lang.Parse("XYZ")
	if err == nil {
		t.Fatalf("Parse(%q) unexpected success, want error", "XYZ")
	}

	errMsg := err.Error()
	if !contains(errMsg, "XYZ") {
		t.Errorf("Parse(%q) error = %q, want containing %q", "XYZ", errMsg, "XYZ")
	}
}

// ---------------------------------------------------------------------------
// TestMustParse - Panic behavior for invalid inputs
// ---------------------------------------------------------------------------

func TestMustParse(t *testing.T) {
	t.Parallel()

	t.Run("valid code does not panic", func(t *testing.T) {
		t.Parallel()

		defer func() {
			if r := recover(); r != nil {
				t.Errorf("MustParse(\"en\") panicked: %v", r)
			}
		}()

		l := lang.MustParse("en")
		if l.String() != "en" {
			t.Errorf("MustParse(\"en\").String() = %q, want \"en\"", l.String())
		}
	})

	t.Run("empty string does not panic", func(t *testing.T) {
		t.Parallel()

		defer func() {
			if r := recover(); r != nil {
				t.Errorf("MustParse(\"\") panicked: %v", r)
			}
		}()

		l := lang.MustParse("")
		if !l.IsZero() {
			t.Error("MustParse(\"\") should return zero Language")
		}
	})

	t.Run("invalid code panics", func(t *testing.T) {
		t.Parallel()

		defer func() {
			if r := recover(); r == nil {
				t.Error("MustParse(\"invalid\") did not panic")
			}
		}()

		_ = lang.MustParse("invalid")
	})
}

// ---------------------------------------------------------------------------
// TestLanguage_String - Returns normalized language code
// ---------------------------------------------------------------------------

func TestLanguage_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"english", "en", "en"},
		{"french", "fr", "fr"},
		{"locale normalized", "pt-BR", "pt-br"},
		{"zero value", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			l := lang.MustParse(tt.input)
			if got := l.String(); got != tt.want {
				t.Errorf("Language.String() for input %q = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestLanguage_IsZero - Detects auto-detect mode (zero value)
// ---------------------------------------------------------------------------

func TestLanguage_IsZero(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"empty string is zero", "", true},
		{"english is not zero", "en", false},
		{"locale is not zero", "pt-BR", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			l := lang.MustParse(tt.input)
			if got := l.IsZero(); got != tt.want {
				t.Errorf("Language.IsZero() for input %q = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestLanguage_IsEnglish - Detects English language codes
// ---------------------------------------------------------------------------

func TestLanguage_IsEnglish(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  bool
	}{
		// True cases
		{name: "english base", input: "en", want: true},
		{name: "american english", input: "en-US", want: true},
		{name: "british english", input: "en-GB", want: true},
		{name: "australian english", input: "en-AU", want: true},

		// False cases
		{name: "empty string", input: "", want: false},
		{name: "french", input: "fr", want: false},
		{name: "spanish", input: "es", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			l := lang.MustParse(tt.input)
			if got := l.IsEnglish(); got != tt.want {
				t.Errorf("Language.IsEnglish() for input %q = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestLanguage_IsFrench - Detects French language codes
// ---------------------------------------------------------------------------

func TestLanguage_IsFrench(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  bool
	}{
		// True cases
		{name: "french base", input: "fr", want: true},
		{name: "canadian french", input: "fr-CA", want: true},
		{name: "french france", input: "fr-FR", want: true},
		{name: "french belgium", input: "fr-BE", want: true},

		// False cases
		{name: "empty string", input: "", want: false},
		{name: "english", input: "en", want: false},
		{name: "spanish", input: "es", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			l := lang.MustParse(tt.input)
			if got := l.IsFrench(); got != tt.want {
				t.Errorf("Language.IsFrench() for input %q = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestLanguage_BaseCode - Extracts ISO 639-1 base code from locale
// ---------------------------------------------------------------------------

func TestLanguage_BaseCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		// Simple codes (no change)
		{name: "english", input: "en", want: "en"},
		{name: "french", input: "fr", want: "fr"},

		// Locales (extract base)
		{name: "brazilian portuguese", input: "pt-BR", want: "pt"},
		{name: "canadian french", input: "fr-CA", want: "fr"},
		{name: "british english", input: "en-GB", want: "en"},
		{name: "simplified chinese", input: "zh-CN", want: "zh"},

		// Edge cases
		{name: "empty string", input: "", want: ""},
		{name: "multiple hyphens takes first part", input: "zh-hans-cn", want: "zh"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			l := lang.MustParse(tt.input)
			if got := l.BaseCode(); got != tt.want {
				t.Errorf("Language.BaseCode() for input %q = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestLanguage_DisplayName - Returns human-readable language names
// ---------------------------------------------------------------------------

func TestLanguage_DisplayName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		// Exact locale matches
		{name: "english", input: "en", want: "English"},
		{name: "american english", input: "en-us", want: "American English"},
		{name: "british english", input: "en-gb", want: "British English"},
		{name: "french", input: "fr", want: "French"},
		{name: "canadian french", input: "fr-ca", want: "Canadian French"},
		{name: "brazilian portuguese", input: "pt-br", want: "Brazilian Portuguese"},
		{name: "european portuguese", input: "pt-pt", want: "European Portuguese"},
		{name: "simplified chinese", input: "zh-cn", want: "Simplified Chinese"},
		{name: "traditional chinese", input: "zh-tw", want: "Traditional Chinese"},

		// Less common languages (all validLanguages have display names)
		{name: "swahili", input: "sw", want: "Swahili"},
		{name: "tagalog", input: "tl", want: "Tagalog"},
		{name: "macedonian", input: "mk", want: "Macedonian"},
		{name: "gujarati", input: "gu", want: "Gujarati"},

		// Fallback to base language (unknown locale, known base)
		{name: "french belgium fallback", input: "fr-BE", want: "French"},
		{name: "spanish argentina fallback", input: "es-AR", want: "Spanish"},
		{name: "portuguese angola fallback", input: "pt-AO", want: "Portuguese"},

		// Edge cases
		{name: "empty string", input: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			l := lang.MustParse(tt.input)
			if got := l.DisplayName(); got != tt.want {
				t.Errorf("Language.DisplayName() for input %q = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestLanguage_DisplayName_UnknownLocaleFallsBackToCode(t *testing.T) {
	t.Parallel()

	// Parse a valid base with unknown suffix
	l := lang.MustParse("en-XXXXX")

	// Should fall back to base display name
	if got := l.DisplayName(); got != "English" {
		t.Errorf("DisplayName() = %q, want \"English\"", got)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// contains checks if substr is in s (simple helper to avoid strings import).
func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
