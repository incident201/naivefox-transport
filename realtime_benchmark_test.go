package transport

import (
	"testing"

	"github.com/incident201/naivefox-transport/internal/cell"
)

func BenchmarkRealtimeEncoding(b *testing.B) {
	for _, capacity := range []int{8192, 65536, cell.MaxCell} {
		for _, duplicate := range []bool{true, false} {
			name := "once"
			if duplicate {
				name = "overwritten"
			}
			b.Run(fmtCapacity(capacity)+"/"+name, func(b *testing.B) {
				frames := []cell.Frame{{Kind: cell.Data, Stream: 1, Body: make([]byte, capacity/2)}}
				b.SetBytes(int64(capacity))
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if duplicate {
						if _, err := cell.Encode(uint32(i), capacity, frames); err != nil {
							b.Fatal(err)
						}
					}
					body, err := cell.EncodeRealtime(uint32(i), capacity, cell.PressureBulk, frames)
					if err != nil || len(body) != capacity {
						b.Fatal("encode failed", err)
					}
				}
			})
		}
	}
}

func fmtCapacity(capacity int) string {
	switch capacity {
	case 8192:
		return "8KiB"
	case 65536:
		return "64KiB"
	default:
		return "256KiB"
	}
}
