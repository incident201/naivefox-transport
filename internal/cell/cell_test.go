package cell

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestReplacementKeepsCapacity(t *testing.T) {
	for _, size := range []int{4096, 24576, 131072} {
		for _, load := range []int{0, 1, 1800, 3000} {
			frames := []Frame{}
			if load > 0 {
				frames = append(frames, Frame{Kind: Data, Stream: 17, Body: bytes.Repeat([]byte{91}, load)})
			}
			encoded, err := Encode(9, size, frames)
			if err != nil || len(encoded) != size {
				t.Fatal(size, load, err)
			}
			seq, decoded, filler, err := Decode(encoded)
			if err != nil || seq != 9 || len(decoded) != len(frames) {
				t.Fatal(seq, err)
			}
			expected := size - Header
			if load > 0 {
				expected -= FrameHeader + load
				if !bytes.Equal(decoded[0].Body, frames[0].Body) {
					t.Fatal("payload")
				}
			}
			if filler != expected {
				t.Fatal("replacement accounting", filler, expected)
			}
		}
	}
}

func TestMalformedCellsFailClosed(t *testing.T) {
	good, _ := Encode(0, 4096, []Frame{{Kind: Data, Stream: 1, Body: []byte("hello")}})
	for _, mutate := range []func([]byte){
		func(b []byte) { b[0] = 0 },
		func(b []byte) { b[15] = 1 },
		func(b []byte) { b[17] = 1 },
		func(b []byte) { b[16] = 99 },
		func(b []byte) { binary.BigEndian.PutUint32(b[8:12], 9000) },
		func(b []byte) { binary.BigEndian.PutUint32(b[28:32], 0xffffffff) },
		func(b []byte) { binary.BigEndian.PutUint16(b[12:14], 2) },
	} {
		b := append([]byte(nil), good...)
		mutate(b)
		if _, _, _, err := Decode(b); err == nil {
			t.Fatal("accepted corruption")
		}
	}
	for n := 0; n < Header+FrameHeader+5; n++ {
		if _, _, _, err := Decode(good[:n]); err == nil {
			t.Fatal("accepted truncation", n)
		}
	}
	if _, err := Encode(0, Header, []Frame{{Kind: Fin, Stream: 1}}); err == nil {
		t.Fatal("accepted overflow")
	}
}
