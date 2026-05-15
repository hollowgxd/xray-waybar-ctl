package subscription

import (
	"encoding/base64"
	"strings"
)

// decodeBase64Loose tries the four flavours of base64 a subscription body
// might use (Std/URL × Padded/Raw). If none parses, the input is assumed
// to already be plain text and returned unchanged.
func decodeBase64Loose(raw string) []byte {
	trimmed := strings.TrimSpace(raw)
	// strip whitespace inside the payload — some servers wrap lines at 76 chars
	cleaned := strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t', ' ':
			return -1
		}
		return r
	}, trimmed)

	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.RawURLEncoding,
	} {
		if out, err := enc.DecodeString(cleaned); err == nil && len(out) > 0 {
			return out
		}
	}
	return []byte(trimmed)
}
