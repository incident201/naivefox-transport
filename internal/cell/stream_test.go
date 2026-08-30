package cell

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"
)

func TestStreamDecoderAllSplitsAndEarlyFrames(t *testing.T) {
	want := []Frame{{Kind: Data, Stream: 1, Body: bytes.Repeat([]byte{42}, 31)}, {Kind: Data, Stream: 1, Sequence: 31, Body: bytes.Repeat([]byte{43}, 23)}, {Kind: Fin, Stream: 1, Sequence: 54}}
	body, _ := Encode(7, 512, want)
	body = body[:binary.BigEndian.Uint32(body[8:12])]
	for split := 0; split <= len(body); split++ {
		d := NewStreamDecoder(7)
		a, err := d.Push(body[:split], false)
		if err != nil {
			t.Fatal(split, err)
		}
		if split >= Header+want[0].Size() && len(a) == 0 {
			t.Fatal("first frame held")
		}
		b, err := d.Push(body[split:], true)
		if err != nil || !reflect.DeepEqual(append(a, b...), want) {
			t.Fatal(split, err)
		}
		if _, err := d.Push(nil, true); err == nil {
			t.Fatal("duplicate final")
		}
	}
	d := NewStreamDecoder(7)
	var got []Frame
	for _, value := range body {
		frames, err := d.Push([]byte{value}, false)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, frames...)
	}
	if _, err := d.Push(nil, true); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal(err)
	}
}

func TestStreamDecoderRejectsCorruptionTruncationAndOverflow(t *testing.T) {
	body, _ := Encode(7, 128, []Frame{{Kind: Data, Stream: 1, Body: []byte("hello")}})
	body = body[:binary.BigEndian.Uint32(body[8:12])]
	for n := 0; n < len(body); n++ {
		if _, err := NewStreamDecoder(7).Push(body[:n], true); err == nil {
			t.Fatal("truncated", n)
		}
	}
	for _, mutate := range []func([]byte){
		func(b []byte) { b[0] = 0 }, func(b []byte) { b[7] = 8 }, func(b []byte) { b[14] = 1 },
		func(b []byte) { b[13] = 2 }, func(b []byte) { b[16] = 9 }, func(b []byte) { b[17] = 1 },
		func(b []byte) { binary.BigEndian.PutUint32(b[8:12], MaxCell+1) },
		func(b []byte) { binary.BigEndian.PutUint32(b[28:32], 0xffffffff) },
	} {
		bad := append([]byte(nil), body...)
		mutate(bad)
		d := NewStreamDecoder(7)
		if _, err := d.Push(bad, true); err == nil {
			t.Fatal("corrupt")
		}
		if _, err := d.Push(body, true); err == nil {
			t.Fatal("decoder reused after error")
		}
	}
	if _, err := NewStreamDecoder(7).Push(make([]byte, MaxCell+1), false); err == nil {
		t.Fatal("unbounded")
	}
	max, _ := Encode(7, MaxCell, []Frame{{Kind: Data, Stream: 1, Body: make([]byte, MaxCell-Header-FrameHeader)}})
	d := NewStreamDecoder(7)
	if frames, err := d.Push(max, true); err != nil || len(frames) != 1 || cap(d.body) > MaxCell {
		t.Fatal("maximum", err)
	}
	empty, _ := Encode(7, Header, nil)
	if frames, err := NewStreamDecoder(7).Push(empty, true); err != nil || len(frames) != 0 {
		t.Fatal("empty", err)
	}
}
