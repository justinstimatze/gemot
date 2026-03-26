// Package sanitize provides input sanitization for agent-submitted content.
// Handles PII filtering and prompt injection detection before content reaches the LLM.
package sanitize

import (
	"regexp"
	"strings"
)

// Result contains the sanitized text and any warnings generated.
type Result struct {
	Text     string
	Warnings []string
}

// Prompt injection patterns adapted from T3C (simple_sanitizer.py).
// These detect common prompt injection attempts. We warn but don't reject,
// because legitimate positions may contain phrases like "ignore previous approaches."
var injectionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bignore\s+(all\s+)?(previous|above|earlier)\s+(instructions?|prompts?)`),
	regexp.MustCompile(`(?i)\b(system|assistant|ai)\s*:\s*`),
	regexp.MustCompile(`(?i)\byou\s+are\s+(now|actually)\s+`),
	regexp.MustCompile(`(?i)\bact\s+as\s+(if\s+)?you\s+(are|were)\s+`),
	regexp.MustCompile(`(?i)\bpretend\s+(to\s+be|you\s+are)\s+`),
	regexp.MustCompile(`(?i)\bforget\s+(all|everything|your)\b`),
	regexp.MustCompile(`(?i)\bnew\s+instructions?\s*:`),
	regexp.MustCompile(`(?i)\boverride\s+(all|previous|system)\b`),
}

// PII patterns for stripping personally identifiable information before LLM calls.
var piiPatterns = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`), "[EMAIL]"},
	{regexp.MustCompile(`\b\d{3}[\s.\-]?\d{3}[\s.\-]?\d{4}\b`), "[PHONE]"},
	{regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`), "[SSN]"},
	{regexp.MustCompile(`\b\d{4}[\s\-]?\d{4}[\s\-]?\d{4}[\s\-]?\d{4}\b`), "[CARD]"},
}

// Position sanitizes agent-submitted position content.
// Returns sanitized text with PII removed and any injection warnings.
func Position(content string) Result {
	r := Result{}

	// Strip PII first (so injection check runs on the text that will actually reach the LLM)
	sanitized := content
	for _, pp := range piiPatterns {
		if pp.pattern.MatchString(sanitized) {
			r.Warnings = append(r.Warnings, "PII_STRIPPED: removed "+pp.replacement+" from position content")
			sanitized = pp.pattern.ReplaceAllString(sanitized, pp.replacement)
		}
	}

	// Normalize whitespace
	sanitized = strings.TrimSpace(sanitized)

	// Check for prompt injection patterns on the sanitized text
	for _, pat := range injectionPatterns {
		if pat.MatchString(sanitized) {
			r.Warnings = append(r.Warnings, "INJECTION_PATTERN: position contains prompt injection pattern: "+pat.String())
		}
	}

	r.Text = sanitized
	return r
}
