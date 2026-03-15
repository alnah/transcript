package template_test

// Notes:
// - Black-box testing: we test through the public API only
// - We deliberately do NOT test prompt content details (fragile, implementation detail)
// - We only verify prompts are non-empty, which is the observable contract
// - Case-sensitivity is a feature: the exported constants are the intended API
// - Name type: tests cover ParseName, MustParseName, String, IsZero, Prompt
// - Pre-parsed constants (BrainstormName, etc.) are tested for consistency

import (
	"errors"
	"testing"

	"github.com/alnah/transcript/internal/template"
)

// ---------------------------------------------------------------------------
// TestGet_ValidTemplates - Happy path: known templates return non-empty prompts
// ---------------------------------------------------------------------------

func TestGet_ValidTemplates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		templateName string
	}{
		{"brainstorm constant", template.Brainstorm},
		{"meeting constant", template.Meeting},
		{"lecture constant", template.Lecture},
		{"notes constant", template.Notes},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			prompt, err := template.Get(tt.templateName)

			if err != nil {
				t.Fatalf("Get(%q) unexpected error: %v", tt.templateName, err)
			}
			if prompt == "" {
				t.Errorf("Get(%q) returned empty prompt", tt.templateName)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestGet_InvalidTemplates - Error cases: unknown names return ErrUnknown
// ---------------------------------------------------------------------------

func TestGet_InvalidTemplates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		templateName string
	}{
		{"unknown name", "unknown"},
		{"empty string", ""},
		{"wrong case uppercase", "BRAINSTORM"},
		{"wrong case mixed", "Brainstorm"},
		{"wrong case meeting", "MEETING"},
		{"with spaces", " brainstorm"},
		{"similar but wrong", "brainstorming"},
		{"special characters", "brain-storm"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			prompt, err := template.Get(tt.templateName)

			if err == nil {
				t.Errorf("Get(%q) expected error, got prompt of length %d", tt.templateName, len(prompt))
			}
			if !errors.Is(err, template.ErrUnknown) {
				t.Errorf("Get(%q) error = %v, want errors.Is(err, ErrUnknown)", tt.templateName, err)
			}
			if prompt != "" {
				t.Errorf("Get(%q) returned non-empty prompt on error: %q", tt.templateName, prompt)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestNames_ReturnsCanonicalOrder - Names returns the documented order
// ---------------------------------------------------------------------------

func TestNames_ReturnsCanonicalOrder(t *testing.T) {
	t.Parallel()

	got := template.Names()
	want := []string{template.Brainstorm, template.Meeting, template.Lecture, template.Notes}

	if len(got) != len(want) {
		t.Fatalf("Names() returned %d elements, want %d", len(got), len(want))
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Names()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// ---------------------------------------------------------------------------
// TestNames_ReturnsCopy - Names returns a defensive copy, not the internal slice
// ---------------------------------------------------------------------------

func TestNames_ReturnsCopy(t *testing.T) {
	t.Parallel()

	// Get first copy and modify it
	first := template.Names()
	original := first[0]
	first[0] = "hacked"

	// Get second copy - should be unaffected
	second := template.Names()

	if second[0] != original {
		t.Errorf("Names() returned shared slice: modification affected subsequent calls")
		t.Errorf("Expected %q, got %q", original, second[0])
	}
}

// ---------------------------------------------------------------------------
// TestConsistency_NamesAndGetAreCoherent - Every name from Names() is valid for Get()
// ---------------------------------------------------------------------------

func TestConsistency_NamesAndGetAreCoherent(t *testing.T) {
	t.Parallel()

	names := template.Names()

	if len(names) == 0 {
		t.Fatal("Names() returned empty slice")
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			prompt, err := template.Get(name)

			if err != nil {
				t.Fatalf("Get(%q) unexpected error: %v", name, err)
			}
			if prompt == "" {
				t.Errorf("Get(%q) returned empty prompt for name returned by Names()", name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestConstants_MatchExpectedValues - Exported constants have expected values
// ---------------------------------------------------------------------------

func TestConstants_MatchExpectedValues(t *testing.T) {
	t.Parallel()

	// This test documents that the constants are lowercase strings
	// If someone changes the constant values, this test will catch it
	tests := []struct {
		name     string
		constant string
		want     string
	}{
		{"Brainstorm", template.Brainstorm, "brainstorm"},
		{"Meeting", template.Meeting, "meeting"},
		{"Lecture", template.Lecture, "lecture"},
		{"Notes", template.Notes, "notes"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if tt.constant != tt.want {
				t.Errorf("template.%s = %q, want %q", tt.name, tt.constant, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestParseName - Validates template name parsing
// ---------------------------------------------------------------------------

func TestParseName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"brainstorm valid", "brainstorm", "brainstorm", false},
		{"meeting valid", "meeting", "meeting", false},
		{"lecture valid", "lecture", "lecture", false},
		{"notes valid", "notes", "notes", false},
		{"empty string returns error", "", "", true},
		{"unknown name returns error", "unknown", "", true},
		{"case sensitive - BRAINSTORM invalid", "BRAINSTORM", "", true},
		{"case sensitive - Meeting invalid", "Meeting", "", true},
		{"with spaces invalid", " brainstorm", "", true},
		{"similar but wrong", "brainstorming", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := template.ParseName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if got.String() != tt.want {
				t.Errorf("ParseName(%q).String() = %q, want %q", tt.input, got.String(), tt.want)
			}
			if tt.wantErr && !errors.Is(err, template.ErrUnknown) {
				t.Errorf("ParseName(%q) error should wrap ErrUnknown, got %v", tt.input, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestMustParseName - Validates panic behavior for invalid inputs
// ---------------------------------------------------------------------------

func TestMustParseName(t *testing.T) {
	t.Parallel()

	t.Run("valid name does not panic", func(t *testing.T) {
		t.Parallel()

		defer func() {
			if r := recover(); r != nil {
				t.Errorf("MustParseName(\"brainstorm\") panicked: %v", r)
			}
		}()

		n := template.MustParseName("brainstorm")
		if n.String() != "brainstorm" {
			t.Errorf("MustParseName(\"brainstorm\").String() = %q, want \"brainstorm\"", n.String())
		}
	})

	t.Run("invalid name panics", func(t *testing.T) {
		t.Parallel()

		defer func() {
			if r := recover(); r == nil {
				t.Error("MustParseName(\"invalid\") did not panic")
			}
		}()

		_ = template.MustParseName("invalid")
	})

	t.Run("empty string panics", func(t *testing.T) {
		t.Parallel()

		defer func() {
			if r := recover(); r == nil {
				t.Error("MustParseName(\"\") did not panic")
			}
		}()

		_ = template.MustParseName("")
	})
}

// ---------------------------------------------------------------------------
// TestName_String - Validates String() method
// ---------------------------------------------------------------------------

func TestName_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		n    template.Name
		want string
	}{
		{"brainstorm", template.BrainstormName, "brainstorm"},
		{"meeting", template.MeetingName, "meeting"},
		{"lecture", template.LectureName, "lecture"},
		{"notes", template.NotesName, "notes"},
		{"zero value", template.Name{}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.n.String(); got != tt.want {
				t.Errorf("Name.String() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestName_IsZero - Validates IsZero() method
// ---------------------------------------------------------------------------

func TestName_IsZero(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		n    template.Name
		want bool
	}{
		{"zero value is zero", template.Name{}, true},
		{"brainstorm is not zero", template.BrainstormName, false},
		{"meeting is not zero", template.MeetingName, false},
		{"lecture is not zero", template.LectureName, false},
		{"notes is not zero", template.NotesName, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.n.IsZero(); got != tt.want {
				t.Errorf("Name.IsZero() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestName_Prompt - Validates Prompt() method
// ---------------------------------------------------------------------------

func TestName_Prompt(t *testing.T) {
	t.Parallel()

	t.Run("valid names return non-empty prompts", func(t *testing.T) {
		t.Parallel()

		names := []template.Name{
			template.BrainstormName,
			template.MeetingName,
			template.LectureName,
			template.NotesName,
		}

		for _, n := range names {
			prompt := n.Prompt()
			if prompt == "" {
				t.Errorf("%s.Prompt() returned empty string", n.String())
			}
		}
	})

	t.Run("zero value panics", func(t *testing.T) {
		t.Parallel()

		defer func() {
			if r := recover(); r == nil {
				t.Error("Name{}.Prompt() did not panic")
			}
		}()

		var n template.Name
		_ = n.Prompt()
	})
}

// ---------------------------------------------------------------------------
// TestName_PreParsedConstants - Pre-parsed constants match parsed values
// ---------------------------------------------------------------------------

func TestName_PreParsedConstants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		constant template.Name
		input    string
	}{
		{"BrainstormName", template.BrainstormName, "brainstorm"},
		{"MeetingName", template.MeetingName, "meeting"},
		{"LectureName", template.LectureName, "lecture"},
		{"NotesName", template.NotesName, "notes"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			parsed, err := template.ParseName(tt.input)
			if err != nil {
				t.Fatalf("ParseName(%q) failed: %v", tt.input, err)
			}
			if parsed != tt.constant {
				t.Errorf("%s != ParseName(%q)", tt.name, tt.input)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestName_PromptConsistentWithGet - Prompt() returns same as deprecated Get()
// ---------------------------------------------------------------------------

func TestName_PromptConsistentWithGet(t *testing.T) {
	t.Parallel()

	names := []string{
		template.Brainstorm,
		template.Meeting,
		template.Lecture,
		template.Notes,
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// Get via deprecated API
			gotOld, err := template.Get(name)
			if err != nil {
				t.Fatalf("Get(%q) failed: %v", name, err)
			}

			// Get via new API
			parsed, err := template.ParseName(name)
			if err != nil {
				t.Fatalf("ParseName(%q) failed: %v", name, err)
			}
			gotNew := parsed.Prompt()

			if gotOld != gotNew {
				t.Errorf("Get(%q) != ParseName(%q).Prompt()", name, name)
			}
		})
	}
}
