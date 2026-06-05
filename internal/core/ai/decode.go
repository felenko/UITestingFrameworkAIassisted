package ai

import (
	"encoding/json"
	"strings"
)

// decodeProviderStdout normalizes a provider subprocess's stdout before verdict parsing.
func decodeProviderStdout(provider, stdout string) string {
	if provider != "cursor" {
		return stdout
	}
	return decodeCursorJSON(stdout)
}

func decodeCursorJSON(stdout string) string {
	stdout = strings.TrimSpace(stdout)
	if !strings.HasPrefix(stdout, "{") {
		return stdout
	}
	var parsed struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil || parsed.Result == "" {
		return stdout
	}
	return parsed.Result
}
