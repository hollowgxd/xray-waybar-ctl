package subscription

import (
	"strings"
)

// EmptyReason avoids dumping a response body or token-bearing URL into logs.
func EmptyReason(body []byte, errs []error) string {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return "endpoint returned an empty body; check the subscription URL and panel format"
	}
	if trimmed == "[]" {
		return "endpoint returned an empty JSON array (no nodes); check subscription status and panel format"
	}
	if isClashYAML(body) && len(errs) == 0 {
		return "Clash subscription contains no supported nodes (proxies is empty)"
	}
	if len(errs) > 0 {
		return "no usable servers: response format is unsupported or all entries are invalid"
	}
	return "no usable servers; supported formats: Clash YAML and vless/vmess/trojan URI lists (plain or base64)"
}
