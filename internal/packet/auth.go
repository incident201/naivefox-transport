package packet

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
)

const ExporterLabel = "EXPORTER-NaiveFox-packet"
const ExporterContext = "naivefox/https"

func Secret(state tls.ConnectionState) ([]byte, error) {
	return state.ExportKeyingMaterial(ExporterLabel, []byte(ExporterContext), 32)
}

// MAC authenticates direction, operation, routing identity, counters and exact
// body. HTTP status or a visible session identifier is never an acknowledgement.
func MAC(secret []byte, operation, session string, sequence, cursor uint64, body []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	for _, field := range [][]byte{[]byte("naivefox-packet"), []byte(operation), []byte(session)} {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(field)))
		mac.Write(length[:])
		mac.Write(field)
	}
	var counters [16]byte
	binary.BigEndian.PutUint64(counters[:8], sequence)
	binary.BigEndian.PutUint64(counters[8:], cursor)
	mac.Write(counters[:])
	digest := sha256.Sum256(body)
	mac.Write(digest[:])
	return mac.Sum(nil)
}

func Verify(secret []byte, operation, session string, sequence, cursor uint64, body, tag []byte) bool {
	return len(secret) == 32 && len(tag) == sha256.Size &&
		hmac.Equal(tag, MAC(secret, operation, session, sequence, cursor, body))
}
