package platformclient

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestCertPinVerifierAcceptsOnlyThePinnedCertificate(t *testing.T) {
	der := []byte("test-certificate-der-bytes")
	sum := sha256.Sum256(der)
	pin := hex.EncodeToString(sum[:])

	if err := certPinVerifier(pin)([][]byte{der}, nil); err != nil {
		t.Fatalf("matching certificate was rejected: %v", err)
	}
	if err := certPinVerifier(strings.ToUpper(pin))([][]byte{der}, nil); err == nil {
		t.Fatal("verifier must reject an uppercase pin instead of matching loosely")
	}
	other := sha256.Sum256([]byte("another-certificate"))
	if err := certPinVerifier(hex.EncodeToString(other[:]))([][]byte{der}, nil); err == nil {
		t.Fatal("mismatched fingerprint must be rejected")
	} else if !strings.Contains(err.Error(), pin) {
		t.Fatalf("mismatch error should include the observed fingerprint, got %v", err)
	}
	if err := certPinVerifier(pin)(nil, nil); err == nil {
		t.Fatal("missing peer certificate must be rejected")
	}
}

func TestNewAcceptsPasswordLengthThePlatformAllows(t *testing.T) {
	// The server accepts 8+ character player passwords; the client must not be
	// stricter, otherwise a valid deployment fails with NOT_CONFIGURED.
	if _, err := New(Config{Address: "127.0.0.1:25664", PlayerPassword: "Yiuk29564"}); err != nil {
		t.Fatalf("8-character password rejected: %v", err)
	}
	if _, err := New(Config{Address: "127.0.0.1:25664", PlayerPassword: "short"}); err == nil {
		t.Fatal("password below the platform floor must be rejected")
	}
}
