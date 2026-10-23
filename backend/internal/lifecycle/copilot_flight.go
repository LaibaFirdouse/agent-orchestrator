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
		if s.ToolUseID != "" {
			return s.ToolUseID == f.copilotClearCandidate
		}
		if f.copilotPromptToolName != "" && strings.EqualFold(s.ToolName, f.copilotPromptToolName) {
			return !copilotSameNameInflightAmbiguous(f, s.ToolName)
		}
		return false
	}
	if f.copilotPromptToolName == "" {
		return false
	}
	if !strings.EqualFold(s.ToolName, f.copilotPromptToolName) {
		return false
	}
	return !copilotSameNameInflightAmbiguous(f, s.ToolName)
}

func copilotBeginAwaitPost(f *toolFlight, s ports.ActivitySignal) {
	if f == nil {
		return
	}
	f.copilotAwaitPostToolUseID = s.ToolUseID
	if f.copilotAwaitPostToolUseID == "" {
		f.copilotAwaitPostToolUseID = f.copilotClearCandidate
	}
	if f.copilotAwaitPostToolUseID != "" {
		f.copilotAwaitPostToolName = ""
		return
	}
	f.copilotAwaitPostToolName = s.ToolName
	if f.copilotAwaitPostToolName == "" {
		f.copilotAwaitPostToolName = f.copilotPromptToolName
	}
}

func copilotPostCompletesAwait(f *toolFlight, s ports.ActivitySignal) bool {
	if f == nil {
		return false
	}
	if f.copilotAwaitPostToolUseID != "" && s.ToolUseID != "" {
		return s.ToolUseID == f.copilotAwaitPostToolUseID
	}
	if f.copilotAwaitPostToolName != "" && s.ToolName != "" {
		return strings.EqualFold(s.ToolName, f.copilotAwaitPostToolName)
	}
	return false
}

func copilotAwaitingPostCompletion(f *toolFlight) bool {
	if f == nil {
		return false
	}
	return f.copilotAwaitPostToolUseID != "" || f.copilotAwaitPostToolName != ""
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
	if copilotAwaitingPostCompletion(fl) {
		return true
	}
	// Strict: after a prompt fully completes (through post), drop async
	// duplicates until Copilot emits a fresh permission-request cycle.
	if fl.copilotConsumedPermReqSeq > 0 && fl.copilotPermReqSeq <= fl.copilotConsumedPermReqSeq {
		return true
	}
	return false
}

func copilotPromotePendingPermissionIdentity(f *toolFlight) {
	if f == nil {
		return
	}
	if f.copilotPendingPromptToolName != "" && f.copilotPendingPermReqSeq == f.copilotPermReqSeq {
		f.copilotPromptToolName = f.copilotPendingPromptToolName
		f.copilotClearCandidate = f.copilotPendingClearCandidate
	}
	f.copilotPendingPermReqSeq = 0
	f.copilotPendingPromptToolName = ""
	f.copilotPendingClearCandidate = ""
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
	candidate := copilotBlockedCandidateFromInflight(fl, s.ToolName, s.ToolUseID)
	if cur == domain.ActivityWaitingInput && fl.copilotPermissionWaiting {
		fl.copilotPromptToolName = s.ToolName
		fl.copilotClearCandidate = candidate
		return
	}
	if cur != domain.ActivityWaitingInput {
		fl.copilotPendingPermReqSeq = fl.copilotPermReqSeq
		fl.copilotPendingPromptToolName = s.ToolName
		fl.copilotPendingClearCandidate = candidate
	}
}
