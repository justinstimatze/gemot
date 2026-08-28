package mcp

import (
	"strings"
	"testing"
)

// TestOAuthPrincipalFromKey_UsesFullHash is the regression test for the
// code-review finding that the principal identity was truncated to 8 bytes
// (64 bits) of the SHA-256 digest — too narrow for a permanent, durable
// identity feeding the credential/reputation graph. It must now be the full
// 32-byte digest, hex-encoded to 64 characters, and still deterministic and
// distinct per key.
func TestOAuthPrincipalFromKey_UsesFullHash(t *testing.T) {
	const prefix = "oauthkey:"
	const fullSHA256HexLen = 64

	p1 := oauthPrincipalFromKey("gmt_one")
	p2 := oauthPrincipalFromKey("gmt_two")

	if !strings.HasPrefix(p1, prefix) {
		t.Fatalf("principal = %q, want %q prefix", p1, prefix)
	}
	hash := strings.TrimPrefix(p1, prefix)
	if len(hash) != fullSHA256HexLen {
		t.Errorf("hash length = %d, want %d (full SHA-256, not truncated)", len(hash), fullSHA256HexLen)
	}
	if p1 == p2 {
		t.Error("different keys produced the same principal")
	}
	if oauthPrincipalFromKey("gmt_one") != p1 {
		t.Error("oauthPrincipalFromKey is not deterministic for the same key")
	}
}
