package chunk

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestSplit(t *testing.T) {
	tests := []struct {
		name       string
		input      []byte
		chunkSize  int
		wantChunks int
	}{
		{
			name:       "empty input",
			input:      []byte{},
			chunkSize:  16,
			wantChunks: 0,
		},
		{
			name:       "input smaller than chunk size",
			input:      []byte("hello"),
			chunkSize:  16,
			wantChunks: 1,
		},
		{
			name:       "input exactly equal to chunk size",
			input:      bytes.Repeat([]byte("A"), 16),
			chunkSize:  16,
			wantChunks: 1,
		},
		{
			name:       "input requiring multiple chunks",
			input:      bytes.Repeat([]byte("X"), 48),
			chunkSize:  16,
			wantChunks: 3,
		},
		{
			name:       "input not evenly divisible by chunk size",
			input:      bytes.Repeat([]byte("Y"), 50),
			chunkSize:  16,
			wantChunks: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks, err := Split(bytes.NewReader(tt.input), tt.chunkSize)
			if err != nil {
				t.Fatalf("Split returned error: %v", err)
			}
			if len(chunks) != tt.wantChunks {
				t.Fatalf("got %d chunks, want %d", len(chunks), tt.wantChunks)
			}

			// Verify each chunk has a correct ID and sequential Index.
			for i, c := range chunks {
				if c.Index != i {
					t.Errorf("chunk %d: Index = %d, want %d", i, c.Index, i)
				}
				hash := sha256.Sum256(c.Data)
				wantID := hex.EncodeToString(hash[:])
				if c.ID != wantID {
					t.Errorf("chunk %d: ID = %s, want %s", i, c.ID, wantID)
				}
			}
		})
	}
}

func TestSplitDeterminism(t *testing.T) {
	input := bytes.Repeat([]byte("deterministic"), 100)

	chunks1, err := Split(bytes.NewReader(input), 64)
	if err != nil {
		t.Fatalf("first Split returned error: %v", err)
	}

	chunks2, err := Split(bytes.NewReader(input), 64)
	if err != nil {
		t.Fatalf("second Split returned error: %v", err)
	}

	if len(chunks1) != len(chunks2) {
		t.Fatalf("different number of chunks: %d vs %d", len(chunks1), len(chunks2))
	}

	for i := range chunks1 {
		if chunks1[i].ID != chunks2[i].ID {
			t.Errorf("chunk %d: IDs differ between runs: %s vs %s",
				i, chunks1[i].ID, chunks2[i].ID)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	inputs := [][]byte{
		{},
		[]byte("short"),
		bytes.Repeat([]byte("round-trip test data "), 500),
	}

	for _, input := range inputs {
		chunks, err := Split(bytes.NewReader(input), 64)
		if err != nil {
			t.Fatalf("Split returned error: %v", err)
		}

		var buf bytes.Buffer
		if err := Reassemble(chunks, &buf); err != nil {
			t.Fatalf("Reassemble returned error: %v", err)
		}

		if !bytes.Equal(buf.Bytes(), input) {
			t.Errorf("round-trip failed: got %d bytes, want %d bytes",
				buf.Len(), len(input))
		}
	}
}
