package mcpattach

import (
	"crypto/sha256"
	"strings"
)

// SecretEnvPodName returns the private run-pod environment variable used to
// carry one MCP server's secretEnv value. MCP servers commonly use the same
// public variable names (for example, GRAFANA_URL), so injecting those names
// directly into the shared agent process would make the first server's secret
// win for every server. The server-and-variable hash keeps each value isolated
// while the readable suffix makes pod specs diagnosable without exposing
// values.
func SecretEnvPodName(serverName, envName string) string {
	key := strings.TrimSpace(serverName) + "\x00" + strings.TrimSpace(envName)
	sum := sha256.Sum256([]byte(key))
	return "GRATEFULAGENTS_MCP_" + strings.ToUpper(fmtHex(sum[:6])) + "_" + sanitizeEnvName(envName)
}

func fmtHex(value []byte) string {
	const digits = "0123456789abcdef"
	result := make([]byte, len(value)*2)
	for i, b := range value {
		result[i*2] = digits[b>>4]
		result[i*2+1] = digits[b&0x0f]
	}
	return string(result)
}

func sanitizeEnvName(name string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	for _, r := range strings.ToUpper(name) {
		if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "SECRET"
	}
	return b.String()
}
