// Package crypto provides age-based encryption and decryption using scrypt passphrases.
package crypto

import (
	"bytes"
	"fmt"
	"io"

	"filippo.io/age"
)

// Encrypt encrypts plaintext using an age scrypt recipient derived from the given passphrase.
func Encrypt(plaintext []byte, passphrase string) ([]byte, error) {
	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return nil, fmt.Errorf("crypto: creating scrypt recipient: %w", err)
	}
	recipient.SetWorkFactor(15)

	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, recipient)
	if err != nil {
		return nil, fmt.Errorf("crypto: initializing encryption: %w", err)
	}

	if _, err := w.Write(plaintext); err != nil {
		return nil, fmt.Errorf("crypto: writing plaintext: %w", err)
	}

	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("crypto: finalizing encryption: %w", err)
	}

	return buf.Bytes(), nil
}

// Decrypt decrypts ciphertext using an age scrypt identity derived from the given passphrase.
func Decrypt(ciphertext []byte, passphrase string) ([]byte, error) {
	identity, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return nil, fmt.Errorf("crypto: creating scrypt identity: %w", err)
	}

	r, err := age.Decrypt(bytes.NewReader(ciphertext), identity)
	if err != nil {
		return nil, fmt.Errorf("crypto: initializing decryption: %w", err)
	}

	plaintext, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("crypto: reading decrypted data: %w", err)
	}

	return plaintext, nil
}
