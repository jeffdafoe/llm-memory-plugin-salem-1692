package llm

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"time"
)

// NewSceneID mints a UUIDv7 string suitable for Request.SceneID and
// PersistRequest.SceneID. memory-api validates scene_id as a strict
// UUID regex on /v1/chat/send, so a hex-only random ID is not enough.
//
// UUIDv7 carries a 48-bit millisecond timestamp in the high bits, so
// scene IDs minted in order sort lexicographically — useful for admin
// dashboards grouping by scene.
//
// Panics on rand.Read failure; crypto/rand is documented to never
// error on platforms we run on, and a silent fallback would mint
// colliding IDs.
func NewSceneID() string {
	var uuid [16]byte

	ms := uint64(time.Now().UnixMilli())
	uuid[0] = byte(ms >> 40)
	uuid[1] = byte(ms >> 32)
	uuid[2] = byte(ms >> 24)
	uuid[3] = byte(ms >> 16)
	uuid[4] = byte(ms >> 8)
	uuid[5] = byte(ms)

	if _, err := rand.Read(uuid[6:]); err != nil {
		panic("llm: rand.Read failed: " + err.Error())
	}

	uuid[6] = (uuid[6] & 0x0F) | 0x70 // version 7
	uuid[8] = (uuid[8] & 0x3F) | 0x80 // variant RFC 4122

	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		binary.BigEndian.Uint32(uuid[0:4]),
		binary.BigEndian.Uint16(uuid[4:6]),
		binary.BigEndian.Uint16(uuid[6:8]),
		binary.BigEndian.Uint16(uuid[8:10]),
		uuid[10:16],
	)
}
