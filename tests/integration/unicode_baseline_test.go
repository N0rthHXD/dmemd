package integration_test

import (
	"testing"

	"golang.org/x/text/unicode/norm"
)

// TestUnicodeNormalizationBaseline confirms the pinned x/text version provides
// the NFC behavior used for document/query normalization.
func TestUnicodeNormalizationBaseline(t *testing.T) {
	t.Parallel()

	decomposed := "e\u0301" // e + combining acute
	if got := norm.NFC.String(decomposed); got != "é" {
		t.Fatalf("NFC(%q) = %q, want %q", decomposed, got, "é")
	}
	const chinese = "中文记忆"
	if got := norm.NFC.String(chinese); got != chinese {
		t.Fatalf("NFC changed Chinese text: %q", got)
	}
}
