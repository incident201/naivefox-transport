package cell

import (
	"encoding/binary"
	"errors"
)

// StreamDecoder accepts only the used prefix, not filler. Its final marker is
// issued by the HTTP owner after validating the complete response including EOF.
// A later error cannot retract frames already delivered; close the peer on error.
type StreamDecoder struct {
	sequence                    uint32
	body                        []byte
	used, count, offset, parsed int
	ended                       bool
}

func NewStreamDecoder(sequence uint32) *StreamDecoder {
	return &StreamDecoder{sequence: sequence, body: make([]byte, 0, MaxCell)}
}

func (d *StreamDecoder) Push(part []byte, final bool) (frames []Frame, err error) {
	bad := errors.New("invalid incremental carrier cell")
	defer func() {
		if err != nil {
			d.ended = true
		}
	}()
	if d.ended || len(part) > MaxCell-len(d.body) {
		return nil, bad
	}
	d.body = append(d.body, part...)
	if d.used == 0 && len(d.body) >= Header {
		b := d.body
		d.used = int(binary.BigEndian.Uint32(b[8:12]))
		d.count = int(binary.BigEndian.Uint16(b[12:14]))
		if string(b[:4]) != "NFC1" || binary.BigEndian.Uint32(b[4:8]) != d.sequence || b[14] != 0 || b[15] != 0 || d.used < Header || d.used > MaxCell || d.count > 4096 {
			return nil, bad
		}
		d.offset = Header
	}
	if d.used != 0 {
		if len(d.body) > d.used {
			return nil, bad
		}
		for d.parsed < d.count && d.offset+FrameHeader <= len(d.body) {
			b := d.body[d.offset:]
			n := uint64(binary.BigEndian.Uint32(b[12:16]))
			if n > uint64(d.used-d.offset-FrameHeader) || b[0] < Open || b[0] > Opened || b[1] != 0 || b[2] != 0 || b[3] != 0 {
				return nil, bad
			}
			if n > uint64(len(b)-FrameHeader) {
				break
			}
			frames = append(frames, Frame{Kind: b[0], Stream: binary.BigEndian.Uint32(b[4:8]), Sequence: binary.BigEndian.Uint32(b[8:12]), Body: append([]byte(nil), b[FrameHeader:FrameHeader+int(n)]...)})
			d.offset += FrameHeader + int(n)
			d.parsed++
		}
		if d.parsed == d.count && d.offset != d.used {
			return nil, bad
		}
		if len(d.body) == d.used && (d.offset != d.used || d.parsed != d.count) {
			return nil, bad
		}
	}
	if final {
		if d.used == 0 || len(d.body) != d.used || d.offset != d.used || d.parsed != d.count {
			return nil, bad
		}
		d.ended = true
	}
	return frames, nil
}
