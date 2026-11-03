package wire

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestFrameStream(t *testing.T) {
	var buf bytes.Buffer
	run, _ := NewFrame(EventRun, cmdID, Command{CommandID: cmdID, Kind: KindStart, RunID: "r", Agent: "a", AgentVersion: "agv",
		Input: &Input{Prompt: "line one\nline two"}})
	for _, f := range []Frame{{Event: EventPing}, run, AckFrame(Ack{CommandID: cmdID, State: AckAccepted})} {
		if err := WriteFrame(&buf, f); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(buf.String(), "\n"); n != 3 {
		t.Fatalf("%d lines for 3 frames: %q", n, buf.String())
	}
	buf.WriteString("\n") // a blank line is skipped
	fr := NewFrameReader(&buf)
	var got []string
	for {
		f, err := fr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, f.Event)
		if f.Event == EventRun {
			var c Command
			if err := f.Decode(&c); err != nil || c.Input.Prompt != "line one\nline two" || f.ID != cmdID {
				t.Errorf("run frame = %+v (%v)", c, err)
			}
		}
	}
	if strings.Join(got, ",") != "ping,run,ack" {
		t.Errorf("frames = %v", got)
	}
}

func TestFrameStreamBounds(t *testing.T) {
	big := strings.Repeat("x", MaxFrameBytes+10) + "\n"
	if _, err := NewFrameReader(strings.NewReader(big)).Read(); !errors.Is(err, ErrFrameTooLarge) {
		t.Errorf("an oversized line: %v", err)
	}
	if _, err := NewFrameReader(strings.NewReader(`{"event":"ping"`)).Read(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("a frame cut by EOF: %v", err)
	}
	if _, err := NewFrameReader(strings.NewReader("not json\n")).Read(); err == nil {
		t.Error("a line that is not a frame was read")
	}
	if _, err := NewFrameReader(strings.NewReader(`{"id":"x"}` + "\n")).Read(); err == nil {
		t.Error("a frame without an event was read")
	}
	if err := WriteFrame(io.Discard, Frame{Event: EventRun, Data: []byte(`"` + strings.Repeat("x", MaxFrameBytes) + `"`)}); !errors.Is(err, ErrFrameTooLarge) {
		t.Errorf("writing an oversized frame: %v", err)
	}
}
