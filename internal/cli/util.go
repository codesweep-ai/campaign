package cli

import (
	"encoding/json"
	"io"
	"strings"

	"github.com/codesweep-ai/campaign/internal/model"
)

func values(m map[string]model.MemberProfile) []model.MemberProfile {
	out := make([]model.MemberProfile, 0, len(m))
	for _, profile := range m {
		out = append(out, profile)
	}
	return out
}

func writeJSON(w io.Writer, v any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(v)
}

// titleWord capitalizes a single lowercase command verb for help text.
func titleWord(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
