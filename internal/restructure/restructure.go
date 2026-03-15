package restructure

import (
	"context"

	"github.com/alnah/transcript/internal/lang"
	"github.com/alnah/transcript/internal/template"
)

// Restructurer transforms raw transcripts into structured markdown using templates.
type Restructurer interface {
	// Restructure transforms a transcript using the specified template.
	// outputLang specifies the output language.
	// Zero value outputLang uses the template's native language (English).
	Restructure(ctx context.Context, transcript string, tmpl template.Name, outputLang lang.Language) (string, error)
}

// Token estimation: conservative for French text (~3.5 chars/token, we use 3).
const defaultCharsPerToken = 3

// estimateTokens estimates the number of tokens in a text.
// Uses len/3 as a conservative estimate for French text.
// English averages ~4 chars/token, French ~3.5 chars/token.
// We use 3 to err on the side of caution.
func estimateTokens(text string) int {
	return len(text) / defaultCharsPerToken
}
