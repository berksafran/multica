package execenv

import (
	"strings"
	"testing"
)

// Parent/Sub-issue Protocol — the brief must teach every issue-bound agent
// two things: (A) notify the parent when this issue finishes, and (B) pick
// `backlog` vs `todo` deliberately when creating sub-issues. The protocol is
// runtime-only (no server-side state sync), so the rules live in the meta
// skill and these tests guard the wording the rest of the design relies on.

func TestParentSubIssueProtocolEmittedForAssignmentTrigger(t *testing.T) {
	t.Parallel()
	ctx := TaskContextForEnv{
		IssueID: "11111111-2222-3333-4444-555555555555",
	}
	out := buildMetaSkillContent("claude", ctx)

	if !strings.Contains(out, "## Parent / Sub-issue Protocol") {
		t.Fatalf("expected Parent / Sub-issue Protocol section in assignment-triggered brief")
	}
	// Behavior A — order, top-level comment, best-effort framing.
	for _, want := range []string{
		"### A. Notify the parent when this issue is finishing",
		"finish your own issue first",
		"`multica issue status <this-issue-id> in_review`",
		"top-level",
		"NO `--parent`",
		"best-effort",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("behavior A missing %q", want)
		}
	}
	// Mention table — every row must be present so loop-prevention rules
	// stay visible.
	for _, want := range []string{
		"Another agent (not you)",
		"The same agent as yourself",
		"Member or squad",
		"`done` or `cancelled`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("mention table missing row %q", want)
		}
	}
	// Behavior B — backlog vs todo decision.
	for _, want := range []string{
		"### B. Choose `backlog` vs `todo` when creating sub-issues",
		"`--status todo` → **start now**",
		"`--status backlog` → **wait**",
		"`multica issue status <child-id> todo`",
		"all `--status todo`",
		"`--status backlog` from the start",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("behavior B missing %q", want)
		}
	}
}

func TestParentSubIssueProtocolEmittedForCommentTrigger(t *testing.T) {
	t.Parallel()
	ctx := TaskContextForEnv{
		IssueID:          "22222222-3333-4444-5555-666666666666",
		TriggerCommentID: "33333333-4444-5555-6666-777777777777",
	}
	out := buildMetaSkillContent("claude", ctx)

	if !strings.Contains(out, "## Parent / Sub-issue Protocol") {
		t.Fatalf("expected Parent / Sub-issue Protocol section in comment-triggered brief")
	}
	// Comment-triggered runs may still be wrapping up sub-issue work, so
	// both behaviors must appear here too.
	if !strings.Contains(out, "### A. Notify the parent when this issue is finishing") {
		t.Errorf("comment-triggered brief missing behavior A heading")
	}
	if !strings.Contains(out, "### B. Choose `backlog` vs `todo` when creating sub-issues") {
		t.Errorf("comment-triggered brief missing behavior B heading")
	}
}

func TestParentSubIssueProtocolSkippedForNonIssueModes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ctx  TaskContextForEnv
	}{
		{
			name: "chat",
			ctx:  TaskContextForEnv{ChatSessionID: "chat-1"},
		},
		{
			name: "quick-create",
			ctx:  TaskContextForEnv{QuickCreatePrompt: "create me an issue"},
		},
		{
			name: "autopilot run-only",
			ctx:  TaskContextForEnv{AutopilotRunID: "run-1"},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := buildMetaSkillContent("claude", tc.ctx)
			if strings.Contains(out, "## Parent / Sub-issue Protocol") {
				t.Errorf("%s mode must NOT emit the Parent / Sub-issue Protocol section", tc.name)
			}
		})
	}
}

// Guardrails for things Elon's review explicitly flagged: no reference to a
// non-existent `multica issue list --parent` command, and no claim that the
// protocol is a stable / guaranteed handshake.
func TestParentSubIssueProtocolHasNoForbiddenClaims(t *testing.T) {
	t.Parallel()
	ctx := TaskContextForEnv{
		IssueID: "44444444-5555-6666-7777-888888888888",
	}
	out := buildMetaSkillContent("claude", ctx)

	for _, banned := range []string{
		"issue list --parent",
		"is a guaranteed handshake",
		"is a reliable handshake",
		"guarantees parent sync",
		"reliable parent sync",
	} {
		if strings.Contains(out, banned) {
			t.Errorf("brief must not contain %q (best-effort only, no inexistent CLI)", banned)
		}
	}
	// The brief must explicitly frame the signal as best-effort so the
	// agent does not assume the parent always sees it.
	if !strings.Contains(out, "best-effort") {
		t.Errorf("brief must explicitly call the parent notification best-effort")
	}
}
