package crypto

import (
	"bytes"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	plaintext := []byte("hello, ghfs encryption!")
	passphrase := "test-passphrase-123"

	ciphertext, err := Encrypt(plaintext, passphrase)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	decrypted, err := Decrypt(ciphertext, passphrase)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}

	if !bytes.Equal(plaintext, decrypted) {
		t.Fatalf("round-trip mismatch: got %q, want %q", decrypted, plaintext)
	}
}

func TestWrongPassphrase(t *testing.T) {
	plaintext := []byte("secret data")
	passphrase := "correct-passphrase"

	ciphertext, err := Encrypt(plaintext, passphrase)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	_, err = Decrypt(ciphertext, "wrong-passphrase")
	if err == nil {
		t.Fatal("Decrypt with wrong passphrase should have failed")
	}
}

func TestEmptyPlaintext(t *testing.T) {
	plaintext := []byte{}
	passphrase := "some-passphrase"

	ciphertext, err := Encrypt(plaintext, passphrase)
	if err != nil {
		t.Fatalf("Encrypt failed for empty plaintext: %v", err)
	}

	decrypted, err := Decrypt(ciphertext, passphrase)
	if err != nil {
		t.Fatalf("Decrypt failed for empty plaintext: %v", err)
	}

	if !bytes.Equal(plaintext, decrypted) {
		t.Fatalf("round-trip mismatch for empty plaintext: got %q, want %q", decrypted, plaintext)
	}
}

func TestDifferentPlaintextsDifferentCiphertexts(t *testing.T) {
	passphrase := "shared-passphrase"

	ct1, err := Encrypt([]byte("plaintext-one"), passphrase)
	if err != nil {
		t.Fatalf("Encrypt plaintext-one failed: %v", err)
	}

	ct2, err := Encrypt([]byte("plaintext-two"), passphrase)
	if err != nil {
		t.Fatalf("Encrypt plaintext-two failed: %v", err)
	}

	if bytes.Equal(ct1, ct2) {
		t.Fatal("different plaintexts produced identical ciphertexts")
	}
}
