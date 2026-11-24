package wire

import (
	"strings"
	"unicode"
)

const (
	SubagentToolName   = "subagent"
	SubagentExplorer   = "explorer"
	SubagentVerifier   = "verifier"
	SubagentCompletion = "subagent_completion"
)

// SubagentUpdate describes a background task owned by one session.
type SubagentUpdate struct {
	ID          string `json:"id"`
	SessionID   string `json:"sessionId,omitempty"`
	SessionFile string `json:"sessionFile,omitempty"`
	ToolCallID  string `json:"toolCallId,omitempty"`
	Role        string `json:"role,omitempty"`
	Description string `json:"description,omitempty"`
	State       string `json:"state"`
	Provider    string `json:"provider,omitempty"`
	Model       string `json:"model,omitempty"`
	Report      string `json:"report,omitempty"`
	Error       string `json:"error,omitempty"`
	EntryID     string `json:"entryId,omitempty"`
	Usage       *Usage `json:"usage,omitempty"`
	DurationMs  int64  `json:"durationMs,omitempty"`
}

// SubagentRoles finds bare role mentions in user-authored input.
func SubagentRoles(text string) []string {
	var roles []string
	var fence rune
	var inline int
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimLeftFunc(line, unicode.IsSpace)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			if fence == rune(trimmed[0]) {
				fence = 0
			} else if fence == 0 {
				fence = rune(trimmed[0])
			}
			continue
		}
		if fence != 0 || strings.HasPrefix(trimmed, ">") {
			continue
		}
		chars := []rune(line)
		for i := 0; i < len(chars); i++ {
			if inline == 0 && chars[i] == '@' && i+1 < len(chars) && chars[i+1] == '"' &&
				(i == 0 || unicode.IsSpace(chars[i-1]) || strings.ContainsRune("([{<\"'`=", chars[i-1])) {
				// A quoted path may itself contain whitespace and an @role spelling.
				i += 2
				for i < len(chars) && chars[i] != '"' {
					if chars[i] == '\\' && i+1 < len(chars) && (chars[i+1] == '"' || chars[i+1] == '\\') {
						i++
					}
					i++
				}
				continue
			}
			if chars[i] == '`' {
				start := i
				for i+1 < len(chars) && chars[i+1] == '`' {
					i++
				}
				width := i - start + 1
				if inline == width {
					inline = 0
				} else if inline == 0 {
					inline = width
				}
				continue
			}
			if inline != 0 || chars[i] != '@' || (i > 0 && !unicode.IsSpace(chars[i-1])) {
				continue
			}
			end := i + 1
			for end < len(chars) && !unicode.IsSpace(chars[end]) && !strings.ContainsRune(",，。:：；;、!?！？", chars[end]) {
				end++
			}
			role := string(chars[i+1 : end])
			if role == SubagentExplorer || role == SubagentVerifier {
				if len(roles) == 0 || (len(roles) == 1 && roles[0] != role) {
					roles = append(roles, role)
				}
			}
		}
	}
	return roles
}
