package service

import "strings"

// sanitizeDBText removes malformed UTF-8 before persisting user text to MySQL.
// The database is utf8mb4, so this only affects already-corrupted byte
// sequences, for example a Chinese character truncated mid-byte by a client.
func sanitizeDBText(s string) string {
	return strings.ToValidUTF8(s, "")
}
