package chunk

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sort"
)

// DefaultChunkSize is 4 MB.
const DefaultChunkSize = 4 * 1024 * 1024

// Chunk represents a content-addressed piece of a file.
type Chunk struct {
	ID    string // hex-encoded SHA256 of Data
	Data  []byte
	Index int
}

// Split reads from r in chunkSize-byte increments and returns the resulting
// chunks. Each chunk's ID is the hex-encoded SHA256 hash of its Data.
func Split(r io.Reader, chunkSize int) ([]Chunk, error) {
	var chunks []Chunk
	buf := make([]byte, chunkSize)
	index := 0

	for {
		n, err := io.ReadFull(r, buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])

			hash := sha256.Sum256(data)
			chunks = append(chunks, Chunk{
				ID:    hex.EncodeToString(hash[:]),
				Data:  data,
				Index: index,
			})
			index++
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}

	return chunks, nil
}

// Reassemble sorts chunks by Index and writes their Data to w in order.
func Reassemble(chunks []Chunk, w io.Writer) error {
	sorted := make([]Chunk, len(chunks))
	copy(sorted, chunks)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Index < sorted[j].Index
	})

	for _, c := range sorted {
		if _, err := w.Write(c.Data); err != nil {
			return err
		}
	}

	return nil
}
