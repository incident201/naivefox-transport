package cell

import (
	"bytes"
	"encoding/binary"
	"fmt"
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

func TestFillerOnlyEncodingPreservesUsefulPrefix(t *testing.T) {
	for _, load := range []int{0, 131072, MaxCell - Header - FrameHeader} {
		frames := []Frame{{Kind: Data, Stream: 1, Body: bytes.Repeat([]byte{91}, load)}}
		a, err := encode(3, MaxCell, frames, false, PressureIdle)
		if err != nil {
			t.Fatal(err)
		}
		b, err := encode(3, MaxCell, frames, true, PressureIdle)
		if err != nil {
			t.Fatal(err)
		}
		used := Header + FrameHeader + load
		if len(a) != len(b) || !bytes.Equal(a[:used], b[:used]) {
			t.Fatal("useful prefix changed")
		}
		if _, _, _, err := Decode(b); err != nil {
			t.Fatal(err)
		}
	}
}

func BenchmarkEncodeFiller(b *testing.B) {
	for _, used := range []int{0, 131072, 245760} {
		for _, suffix := range []bool{false, true} {
			b.Run(fmt.Sprintf("used%d/suffix%t", used, suffix), func(b *testing.B) {
				frames := []Frame{}
				if used > 0 {
					frames = append(frames, Frame{Kind: Data, Stream: 1, Body: make([]byte, used)})
				}
				b.SetBytes(MaxCell)
				b.ReportAllocs()
				for b.Loop() {
					if _, err := encode(1, MaxCell, frames, suffix, PressureIdle); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func TestRealtimePressureHintIsIsolatedFromHTTPCodec(t *testing.T) {
	for _, hint := range []PressureHint{PressureIdle, PressureInteractive, PressureBulk} {
		body, err := EncodeRealtime(7, 16384, hint, []Frame{{Kind: Credit, Stream: 1, Body: Uint32(4096)}})
		if err != nil {
			t.Fatal(err)
		}
		sequence, frames, _, decodedHint, err := DecodeRealtime(body)
		if err != nil || sequence != 7 || decodedHint != hint || len(frames) != 1 {
			t.Fatalf("realtime round trip: hint=%d decoded=%d error=%v", hint, decodedHint, err)
		}
		if hint != PressureIdle {
			if _, _, _, err := Decode(body); err == nil {
				t.Fatal("HTTP codec accepted realtime pressure hint")
			}
		}
	}
	bad, _ := EncodeRealtime(0, 512, PressureIdle, nil)
	bad[14] = byte(PressureBulk + 1)
	if _, _, _, _, err := DecodeRealtime(bad); err == nil {
		t.Fatal("accepted unknown realtime pressure hint")
	}
	if _, err := EncodeRealtime(0, 512, PressureBulk+1, nil); err == nil {
		t.Fatal("encoded unknown realtime pressure hint")
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
