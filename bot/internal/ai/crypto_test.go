package ai

import "testing"

func TestEncryptDecryptAPIKeyRoundTrip(t *testing.T) {
	if err := ConfigureEncryption("unit-test-key", nil); err != nil {
		t.Fatal(err)
	}
	ciphertext, err := EncryptAPIKey("secret-token")
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := DecryptAPIKey(ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	if plaintext != "secret-token" {
		t.Fatalf("DecryptAPIKey() = %q, want %q", plaintext, "secret-token")
	}
}
