package cell

import (
	"fmt"
	"testing"
)

func BenchmarkEncode(b *testing.B) {
	for _, capacity := range []int{8192, 65536, 262144} {
		for _, fill := range []int{0, 50, 100} {
			b.Run(fmt.Sprintf("%d/%d", capacity, fill), func(b *testing.B) {
				frames := []Frame{}
				if fill > 0 {
					frames = append(frames, Frame{Kind: Data, Stream: 1, Body: make([]byte, (capacity-Header-FrameHeader)*fill/100)})
				}
				b.SetBytes(int64(capacity))
				b.ReportAllocs()
				for b.Loop() {
					if _, err := Encode(0, capacity, frames); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
