package cell

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
)

const (
	Header           = 16
	FrameHeader      = 16
	MaxCell          = 256 * 1024
	Window           = 256 * 1024
	MaxStreams       = 32
	Open        byte = 1
	Data        byte = 2
	Fin         byte = 3
	Reset       byte = 4
	Credit      byte = 5
	Auth        byte = 6
	Opened      byte = 7
)

type Frame struct {
	Kind     byte
	Stream   uint32
	Sequence uint32
	Body     []byte
}

func (f Frame) Size() int { return FrameHeader + len(f.Body) }

func Encode(sequence uint32, capacity int, frames []Frame) ([]byte, error) {
	if capacity < Header || capacity > MaxCell || len(frames) > 4096 {
		return nil, errors.New("invalid cell capacity")
	}
	used := Header
	for _, f := range frames {
		used += f.Size()
	}
	if used > capacity {
		return nil, errors.New("cell overflow")
	}
	body := make([]byte, capacity)
	if _, err := rand.Read(body); err != nil {
		return nil, err
	}
	copy(body, "NFC1")
	binary.BigEndian.PutUint32(body[4:8], sequence)
	binary.BigEndian.PutUint32(body[8:12], uint32(used))
	binary.BigEndian.PutUint16(body[12:14], uint16(len(frames)))
	body[14], body[15] = 0, 0
	pos := Header
	for _, f := range frames {
		body[pos], body[pos+1], body[pos+2], body[pos+3] = f.Kind, 0, 0, 0
		binary.BigEndian.PutUint32(body[pos+4:pos+8], f.Stream)
		binary.BigEndian.PutUint32(body[pos+8:pos+12], f.Sequence)
		binary.BigEndian.PutUint32(body[pos+12:pos+16], uint32(len(f.Body)))
		copy(body[pos+FrameHeader:], f.Body)
		pos += f.Size()
	}
	return body, nil
}

func Decode(body []byte) (uint32, []Frame, int, error) {
	bad := errors.New("invalid carrier cell")
	if len(body) < Header || len(body) > MaxCell || string(body[:4]) != "NFC1" || body[14] != 0 || body[15] != 0 {
		return 0, nil, 0, bad
	}
	seq := binary.BigEndian.Uint32(body[4:8])
	used := int(binary.BigEndian.Uint32(body[8:12]))
	count := int(binary.BigEndian.Uint16(body[12:14]))
	if used < Header || used > len(body) || count > 4096 {
		return 0, nil, 0, bad
	}
	frames := make([]Frame, 0, count)
	pos := Header
	for range count {
		if pos+FrameHeader > used {
			return 0, nil, 0, bad
		}
		length := uint64(binary.BigEndian.Uint32(body[pos+12 : pos+16]))
		if length > uint64(used-pos-FrameHeader) || body[pos] < Open || body[pos] > Opened || body[pos+1] != 0 || body[pos+2] != 0 || body[pos+3] != 0 {
			return 0, nil, 0, bad
		}
		f := Frame{Kind: body[pos], Stream: binary.BigEndian.Uint32(body[pos+4 : pos+8]), Sequence: binary.BigEndian.Uint32(body[pos+8 : pos+12])}
		f.Body = append([]byte(nil), body[pos+FrameHeader:pos+FrameHeader+int(length)]...)
		frames = append(frames, f)
		pos += f.Size()
	}
	if pos != used {
		return 0, nil, 0, bad
	}
	return seq, frames, len(body) - used, nil
}

func Uint32(n uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, n)
	return b
}
