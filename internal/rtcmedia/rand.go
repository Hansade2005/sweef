package rtcmedia

import (
	"crypto/rand"
	"encoding/binary"
)

func randUint32() uint32 {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return binary.BigEndian.Uint32(b[:])
}
