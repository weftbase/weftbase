package wire

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// On a socket (the local placement) frames travel as newline-delimited
// JSON: one Frame object per line. encoding/json escapes every newline
// inside a string, so a line is exactly one frame.

// ErrFrameTooLarge is FrameReader.Read's error for a line over
// MaxFrameBytes: the stream cannot be resynchronised and ends.
var ErrFrameTooLarge = errors.New("wire: frame larger than 8 MiB")

// WriteFrame writes f as one line. The caller serialises writers.
func WriteFrame(w io.Writer, f Frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("wire: frame %s: %w", f.Event, err)
	}
	if len(b) > MaxFrameBytes {
		return ErrFrameTooLarge
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// FrameReader reads newline-delimited frames.
type FrameReader struct {
	r *bufio.Reader
}

// NewFrameReader reads frames from r.
func NewFrameReader(r io.Reader) *FrameReader {
	return &FrameReader{r: bufio.NewReaderSize(r, 64<<10)}
}

// Read returns the next frame. A blank line is skipped; a line that is
// not a frame is an error (the stream is then not trusted); a line
// longer than MaxFrameBytes is ErrFrameTooLarge; a frame cut by EOF is
// io.ErrUnexpectedEOF.
func (fr *FrameReader) Read() (Frame, error) {
	for {
		var line []byte
		for {
			chunk, err := fr.r.ReadSlice('\n')
			line = append(line, chunk...)
			if len(line) > MaxFrameBytes+1 {
				return Frame{}, ErrFrameTooLarge
			}
			if err == nil {
				break
			}
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			if errors.Is(err, io.EOF) && len(bytes.TrimSpace(line)) > 0 {
				return Frame{}, io.ErrUnexpectedEOF
			}
			return Frame{}, err
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var f Frame
		if err := json.Unmarshal(line, &f); err != nil {
			return Frame{}, fmt.Errorf("wire: malformed frame: %w", err)
		}
		if f.Event == "" {
			return Frame{}, errors.New("wire: a frame without an event")
		}
		return f, nil
	}
}
