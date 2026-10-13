package uploads

import (
	"crypto/sha1"
	"fmt"
)

// Owner commands use a stable RFC 4122 version-5 UUID, including owners whose
// public API only accepts UUID idempotency keys. Names never contain credentials.
func ownerCommand(action, resource, command string) string {
	namespace := []byte{0x6b, 0xa7, 0xb8, 0x11, 0x9d, 0xad, 0x11, 0xd1, 0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8}
	h := sha1.New()
	h.Write(namespace)
	h.Write([]byte("cpm/uploads/" + action + "/" + resource + "/" + command))
	b := h.Sum(nil)[:16]
	b[6] = (b[6] & 0x0f) | 0x50
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func creationReplaySafe(kind string) bool {
	if stagedType(kind) {
		return true
	}
	switch kind {
	case "users.legacy", "cards.xlsx", "cards.transform", "exams.questions", "exams.assignments", "exams.outside":
		return true
	}
	return false
}
