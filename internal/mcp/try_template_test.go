package mcp

import (
	"html/template"
	"strings"
	"testing"
)

// TestTryCodeTemplateEmbedded catches two classes of deploy bugs:
// an embed path that drops the template file, and a template syntax
// error that would otherwise only surface on the first user request.
// Eager parsing in RunHTTP relies on this file being parseable.
func TestTryCodeTemplateEmbedded(t *testing.T) {
	if _, err := template.ParseFS(staticFS, "static/try-code.html"); err != nil {
		t.Fatalf("static/try-code.html failed to parse — embed or syntax broken: %v", err)
	}
	if _, err := staticFS.ReadFile("static/try-form.html"); err != nil {
		t.Fatalf("static/try-form.html missing from embed: %v", err)
	}
	if _, err := staticFS.ReadFile("static/index.html"); err != nil {
		t.Fatalf("static/index.html missing from embed: %v", err)
	}
}

// TestTryCodeTemplateRender pins the user-visible contract of the
// sandbox page: the watch-live URL is present, the join code appears
// in every required spot, the topic is HTML-escaped (no raw script
// injection), and the prompt-injection framing is in the copy-msg.
// Any of these regressing silently would undercut the whole pivot.
func TestTryCodeTemplateRender(t *testing.T) {
	tmpl, err := template.ParseFS(staticFS, "static/try-code.html")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, tryCodeData{
		Topic:     `microservices<script>alert(1)</script>`,
		Code:      "bold-latch-123",
		HoursLeft: 47,
	}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := buf.String()

	// Watch-live link points to vis.gemot.dev with the code.
	if !strings.Contains(out, "https://vis.gemot.dev/watch/bold-latch-123") {
		t.Errorf("output missing watch-live URL with code")
	}
	// Code appears in the agent-join instruction.
	if !strings.Contains(out, "<strong>bold-latch-123</strong>") {
		t.Errorf("output missing join-code strong-emphasis")
	}
	// Code appears in the invite block as the watch URL.
	if !strings.Contains(out, "Watch it live: https://vis.gemot.dev/watch/bold-latch-123") {
		t.Errorf("output missing watch URL in invite block")
	}
	// Topic is HTML-escaped — raw <script> must not appear.
	if strings.Contains(out, "<script>alert(1)</script>") {
		t.Errorf("raw <script> leaked through escaping — XSS regression")
	}
	// Escaped form must be present (context-aware engine escapes <, >, etc.).
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Errorf("expected escaped topic in output; got: %q", out)
	}
	// Prompt-injection framing is in the agent-message block.
	if !strings.Contains(out, "untrusted input from whoever created the sandbox") {
		t.Errorf("copy-msg missing prompt-injection framing")
	}
	// Hours-remaining is rendered.
	if !strings.Contains(out, "47h remaining") {
		t.Errorf("output missing hours-remaining display")
	}
}
