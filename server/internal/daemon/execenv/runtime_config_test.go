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

// Comment-triggered briefs must NOT include the unconditional
// `multica issue status <this-issue-id> in_review` instruction from Step A.
// That instruction conflicts with the comment-triggered workflow rule
// "Do NOT change the issue status unless the comment explicitly asks for it"
// (Elon's blocking review on PR #2918). Step A for comment-triggered runs
// must instead remind the agent that the existing status guardrail still
// applies and that the parent notification is gated on actually closing
// out child work.
func TestCommentTriggeredProtocolDoesNotForceInReview(t *testing.T) {
	t.Parallel()
	ctx := TaskContextForEnv{
		IssueID:          "55555555-6666-7777-8888-999999999999",
		TriggerCommentID: "66666666-7777-8888-9999-aaaaaaaaaaaa",
	}
	out := buildMetaSkillContent("claude", ctx)

	// The exact unconditional status-flip command from the previous Step A
	// must not appear anywhere in a comment-triggered brief. It is fine
	// for Step B to teach the agent to *promote* a child to `todo` — that
	// targets a different issue id, so the substring does not collide.
	if strings.Contains(out, "`multica issue status <this-issue-id> in_review`") {
		t.Errorf("comment-triggered brief must not contain the unconditional `multica issue status <this-issue-id> in_review` command from Step A (conflicts with the comment-triggered \"do not change status unless asked\" rule)")
	}

	// The existing comment-triggered workflow rule must still be present
	// AND Step A must echo it, so the agent cannot rely on the rule
	// having been forgotten by the time it reaches the protocol section.
	// Counting occurrences guards against future edits that drop the
	// in-protocol reminder while leaving the workflow rule intact.
	const guardrail = "Do NOT change the issue status unless the comment explicitly asks for it"
	if got := strings.Count(out, guardrail); got < 2 {
		t.Errorf("expected the comment-triggered status guardrail %q to appear at least twice (once in the comment-triggered workflow, once echoed inside protocol Step A), got %d", guardrail, got)
	}

	// And Step A must explicitly gate the parent-notification on
	// actually closing out child work so the agent does not blindly post
	// to the parent on every comment-triggered run.
	for _, want := range []string{
		"closing out the child",
		"skip the parent notification",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("comment-triggered Step A missing required phrasing %q", want)
		}
	}
}

// Assignment-triggered briefs are the inverse boundary: when the agent
// owns the issue lifecycle, Step A must still keep the unconditional
// `multica issue status <this-issue-id> in_review` flip. Splitting Step A
// by trigger type must not silently drop this behavior on the assignment
// branch.
func TestAssignmentTriggeredProtocolStillFlipsInReview(t *testing.T) {
	t.Parallel()
	ctx := TaskContextForEnv{
		IssueID: "77777777-8888-9999-aaaa-bbbbbbbbbbbb",
	}
	out := buildMetaSkillContent("claude", ctx)

	if !strings.Contains(out, "`multica issue status <this-issue-id> in_review`") {
		t.Errorf("assignment-triggered Step A must keep the unconditional `multica issue status <this-issue-id> in_review` flip")
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
