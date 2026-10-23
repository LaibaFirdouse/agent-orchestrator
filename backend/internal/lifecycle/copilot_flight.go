package lifecycle

import (
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// copilotBlockedCandidateFromInflight picks the unique in-flight tool_use_id
// for a permission dialog's tool_name. Empty when ambiguous or unmatched — the
// same fail-closed rule as ActivityBlocked correlation.
func copilotBlockedCandidateFromInflight(f *toolFlight, toolName, toolUseID string) string {
	if f == nil {
		return ""
	}
	if toolUseID != "" {
		if _, ok := f.inflight[toolUseID]; ok {
			return toolUseID
		}
	}
	if toolName == "" {
		return ""
	}
	candidate := ""
	for useID, name := range f.inflight {
		if !strings.EqualFold(name, toolName) {
			continue
		}
		if candidate != "" {
			return ""
		}
		candidate = useID
	}
	return candidate
}

func copilotSameNameInflightAmbiguous(f *toolFlight, toolName string) bool {
	if f == nil || toolName == "" {
		return false
	}
	count := 0
	for _, name := range f.inflight {
		if strings.EqualFold(name, toolName) {
			count++
			if count > 1 {
				return true
			}
		}
	}
	return false
}

// copilotPreToolUseClearsPermission reports whether pre-tool-use proves the
// user answered the Copilot permission notification currently open.
func copilotPreToolUseClearsPermission(f *toolFlight, s ports.ActivitySignal) bool {
	if f == nil || !f.copilotPermissionWaiting {
		return false
	}
	if f.copilotClearCandidate != "" {
		return s.ToolUseID != "" && s.ToolUseID == f.copilotClearCandidate
	}
	if f.copilotPromptToolName == "" {
		return false
	}
	if !strings.EqualFold(s.ToolName, f.copilotPromptToolName) {
		return false
	}
	return !copilotSameNameInflightAmbiguous(f, s.ToolName)
}

func copilotAwaitPostToolUseID(f *toolFlight, s ports.ActivitySignal) string {
	if s.ToolUseID != "" {
		return s.ToolUseID
	}
	if f.copilotClearCandidate != "" {
		return f.copilotClearCandidate
	}
	return ""
}

func copilotShouldSuppressPermissionNotification(
	cur domain.ActivityState,
	fl *toolFlight,
	afterStop bool,
) bool {
	if afterStop {
		return true
	}
	if fl == nil {
		return false
	}
	if cur == domain.ActivityWaitingInput {
		return false
	}
	if fl.copilotAwaitPostToolUseID != "" {
		return true
	}
	// Strict: after a prompt fully completes (through post), drop async
	// duplicates until Copilot emits a fresh permission-request cycle.
	if fl.copilotConsumedPermReqSeq > 0 && fl.copilotPermReqSeq <= fl.copilotConsumedPermReqSeq {
		return true
	}
	return false
}

func (m *Manager) recordCopilotPermissionRequestLocked(id domain.SessionID, cur domain.ActivityState, s ports.ActivitySignal) {
	if s.Event != "permission-request" {
		return
	}
	fl := m.flights[id]
	if fl == nil {
		fl = &toolFlight{inflight: map[string]string{}}
		m.flights[id] = fl
	}
	fl.copilotPermReqSeq++
	if cur == domain.ActivityWaitingInput && fl.copilotPermissionWaiting {
		fl.copilotPromptToolName = s.ToolName
		fl.copilotClearCandidate = copilotBlockedCandidateFromInflight(fl, s.ToolName, s.ToolUseID)
	}
}
