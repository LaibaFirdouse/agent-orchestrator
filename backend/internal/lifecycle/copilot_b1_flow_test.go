package lifecycle

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func sigMeta(event, toolName, toolUseID string) ports.ActivitySignal {
	return ports.ActivitySignal{Valid: false, Event: event, ToolName: toolName, ToolUseID: toolUseID}
}

// End-to-end Copilot B1 (#6280) activity sequence through the lifecycle reducer.
func TestCopilotB1_CompleteActivityFlows(t *testing.T) {
	t.Run("ordinary tool never needs input", func(t *testing.T) {
		m, st, _ := newManager()
		seedSignaled(st, "c1", domain.ActivityIdle)

		mustApply(t, m, "c1", sig(domain.ActivityActive, "session-start", "", ""))
		mustApply(t, m, "c1", sig(domain.ActivityActive, "user-prompt-submit", "", ""))
		mustApply(t, m, "c1", sig(domain.ActivityActive, "pre-tool-use", "bash", ""))
		if got := stateOf(st, "c1"); got != domain.ActivityActive {
			t.Fatalf("after pre-tool-use = %q, want active (not waiting_input)", got)
		}
		mustApply(t, m, "c1", sig(domain.ActivityActive, "post-tool-use", "bash", ""))
		if got := stateOf(st, "c1"); got != domain.ActivityActive {
			t.Fatalf("after post-tool-use = %q, want active", got)
		}
		mustApply(t, m, "c1", sig(domain.ActivityIdle, "stop", "", ""))
		if got := stateOf(st, "c1"); got != domain.ActivityIdle {
			t.Fatalf("after stop = %q, want idle", got)
		}
	})

	t.Run("permission request before notification then approve through post", func(t *testing.T) {
		m, st, _ := newManager()
		seedSignaled(st, "c2a", domain.ActivityActive)

		mustApply(t, m, "c2a", sigMeta("permission-request", "bash", ""))
		mustApply(t, m, "c2a", sig(domain.ActivityWaitingInput, "notification", "", ""))
		if got := stateOf(st, "c2a"); got != domain.ActivityWaitingInput {
			t.Fatalf("after notification = %q, want waiting_input", got)
		}
		mustApply(t, m, "c2a", sig(domain.ActivityActive, "pre-tool-use", "bash", "toolu_bash"))
		if got := stateOf(st, "c2a"); got != domain.ActivityActive {
			t.Fatalf("after correlated pre = %q, want active", got)
		}
		mustApply(t, m, "c2a", sig(domain.ActivityActive, "post-tool-use", "bash", "toolu_bash"))
		if got := stateOf(st, "c2a"); got != domain.ActivityActive {
			t.Fatalf("after gated post = %q, want active", got)
		}
	})

	t.Run("permission prompt then approve then clear", func(t *testing.T) {
		m, st, _ := newManager()
		seedSignaled(st, "c2", domain.ActivityActive)

		mustApply(t, m, "c2", sig(domain.ActivityWaitingInput, "notification", "", ""))
		mustApply(t, m, "c2", sigMeta("permission-request", "bash", ""))
		if got := stateOf(st, "c2"); got != domain.ActivityWaitingInput {
			t.Fatalf("after permission_prompt = %q, want waiting_input", got)
		}
		mustApply(t, m, "c2", sig(domain.ActivityActive, "post-tool-use", "read", "toolu_bg"))
		if got := stateOf(st, "c2"); got != domain.ActivityWaitingInput {
			t.Fatalf("sibling post must not demote waiting_input: %q", got)
		}
		mustApply(t, m, "c2", sig(domain.ActivityActive, "pre-tool-use", "read", "toolu_bg"))
		if got := stateOf(st, "c2"); got != domain.ActivityWaitingInput {
			t.Fatalf("sibling pre must not demote waiting_input: %q", got)
		}
		mustApply(t, m, "c2", sig(domain.ActivityActive, "pre-tool-use", "bash", "toolu_bash"))
		if got := stateOf(st, "c2"); got != domain.ActivityActive {
			t.Fatalf("after approve/pre-tool-use = %q, want active", got)
		}
	})

	t.Run("two sequential approvals in one turn", func(t *testing.T) {
		m, st, _ := newManager()
		seedSignaled(st, "c2b", domain.ActivityActive)

		mustApply(t, m, "c2b", sig(domain.ActivityWaitingInput, "notification", "", ""))
		mustApply(t, m, "c2b", sigMeta("permission-request", "bash", ""))
		mustApply(t, m, "c2b", sig(domain.ActivityActive, "pre-tool-use", "bash", "toolu_bash"))
		mustApply(t, m, "c2b", sig(domain.ActivityActive, "post-tool-use", "bash", "toolu_bash"))
		if got := stateOf(st, "c2b"); got != domain.ActivityActive {
			t.Fatalf("after first post = %q, want active", got)
		}
		mustApply(t, m, "c2b", sigMeta("permission-request", "node", ""))
		mustApply(t, m, "c2b", sig(domain.ActivityWaitingInput, "notification", "", ""))
		if got := stateOf(st, "c2b"); got != domain.ActivityWaitingInput {
			t.Fatalf("second approval = %q, want waiting_input", got)
		}
		mustApply(t, m, "c2b", sig(domain.ActivityActive, "pre-tool-use", "node", "toolu_node"))
		mustApply(t, m, "c2b", sig(domain.ActivityActive, "post-tool-use", "node", "toolu_node"))
		if got := stateOf(st, "c2b"); got != domain.ActivityActive {
			t.Fatalf("after second post = %q, want active", got)
		}
	})

	t.Run("elicitation clears on next prompt", func(t *testing.T) {
		m, st, _ := newManager()
		seedSignaled(st, "c3", domain.ActivityActive)

		mustApply(t, m, "c3", sig(domain.ActivityWaitingInput, "notification", "", ""))
		mustApply(t, m, "c3", sig(domain.ActivityActive, "user-prompt-submit", "", ""))
		if got := stateOf(st, "c3"); got != domain.ActivityActive {
			t.Fatalf("after user answer = %q, want active", got)
		}
	})

	t.Run("late permission_prompt before gated post is suppressed", func(t *testing.T) {
		m, st, _ := newManager()
		seedSignaled(st, "c4", domain.ActivityActive)

		mustApply(t, m, "c4", sig(domain.ActivityWaitingInput, "notification", "", ""))
		mustApply(t, m, "c4", sigMeta("permission-request", "bash", ""))
		mustApply(t, m, "c4", sig(domain.ActivityActive, "pre-tool-use", "bash", "toolu_bash"))
		if got := stateOf(st, "c4"); got != domain.ActivityActive {
			t.Fatalf("after clear = %q, want active", got)
		}
		mustApply(t, m, "c4", sig(domain.ActivityWaitingInput, "notification", "", ""))
		if got := stateOf(st, "c4"); got != domain.ActivityActive {
			t.Fatalf("late notification before post = %q, want active (await post)", got)
		}
	})

	t.Run("late permission_prompt after gated post is suppressed", func(t *testing.T) {
		m, st, _ := newManager()
		seedSignaled(st, "c4b", domain.ActivityActive)

		mustApply(t, m, "c4b", sig(domain.ActivityWaitingInput, "notification", "", ""))
		mustApply(t, m, "c4b", sigMeta("permission-request", "bash", ""))
		mustApply(t, m, "c4b", sig(domain.ActivityActive, "pre-tool-use", "bash", "toolu_bash"))
		mustApply(t, m, "c4b", sig(domain.ActivityActive, "post-tool-use", "bash", "toolu_bash"))
		mustApply(t, m, "c4b", sig(domain.ActivityWaitingInput, "notification", "", ""))
		if got := stateOf(st, "c4b"); got != domain.ActivityActive {
			t.Fatalf("late notification after post = %q, want active (strict stale)", got)
		}
	})

	t.Run("late permission_prompt after stop is suppressed", func(t *testing.T) {
		m, st, _ := newManager()
		seedSignaled(st, "c5", domain.ActivityActive)

		mustApply(t, m, "c5", sig(domain.ActivityWaitingInput, "notification", "", ""))
		mustApply(t, m, "c5", sig(domain.ActivityIdle, "stop", "", ""))
		if got := stateOf(st, "c5"); got != domain.ActivityIdle {
			t.Fatalf("after stop = %q, want idle", got)
		}
		mustApply(t, m, "c5", sig(domain.ActivityWaitingInput, "notification", "", ""))
		if got := stateOf(st, "c5"); got != domain.ActivityIdle {
			t.Fatalf("late notification after stop = %q, want idle", got)
		}
	})
}
