package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestComputerDeleteCannotBeEscalatedThroughPATDaemonIDReuse(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	ownerID := createHandlerTestMember(t, "member")
	attackerID := createHandlerTestMember(t, "member")
	daemonID := "computer-delete-guard-" + uuid.NewString()
	victimProvider := "claude"
	attackerProvider := "codex"

	var victimRuntimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status,
			device_info, metadata, owner_id, last_seen_at
		)
		VALUES ($1, $2, 'victim-runtime', 'local', $3, 'online', '', '{}'::jsonb, $4, now())
		RETURNING id
	`, testWorkspaceID, daemonID, victimProvider, ownerID).Scan(&victimRuntimeID); err != nil {
		t.Fatalf("seed victim runtime: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE daemon_id = $1`, daemonID)
	})

	tokenHash := "computer-delete-token-" + uuid.NewString()
	if _, err := testPool.Exec(ctx, `
		INSERT INTO daemon_token (
			token_hash, workspace_id, daemon_id, expires_at,
			created_by_user_id, install_source
		)
		VALUES ($1, $2, $3, now() + interval '1 hour', $4, 'script')
	`, tokenHash, testWorkspaceID, daemonID, ownerID); err != nil {
		t.Fatalf("seed daemon token: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM daemon_token WHERE token_hash = $1`, tokenHash)
	})

	registerW := httptest.NewRecorder()
	testHandler.DaemonRegister(registerW, newRequestAs(attackerID, http.MethodPost, "/api/daemon/register", map[string]any{
		"workspace_id": testWorkspaceID,
		"daemon_id":    daemonID,
		"device_name":  "attacker-machine",
		"runtimes": []map[string]any{
			{"name": "attacker-runtime", "type": attackerProvider, "version": "1.0.0", "status": "online"},
		},
	}))
	if registerW.Code != http.StatusForbidden {
		t.Fatalf("PAT register with reused daemon_id: expected 403, got %d: %s", registerW.Code, registerW.Body.String())
	}

	var attackerRuntimeCount int
	if err := testPool.QueryRow(ctx, `
		SELECT COUNT(*) FROM agent_runtime
		WHERE workspace_id = $1 AND daemon_id = $2 AND provider = $3
	`, testWorkspaceID, daemonID, attackerProvider).Scan(&attackerRuntimeCount); err != nil {
		t.Fatalf("count attacker runtime: %v", err)
	}
	if attackerRuntimeCount != 0 {
		t.Fatalf("expected PAT takeover register to create no attacker runtime, got %d", attackerRuntimeCount)
	}

	var attackerRuntimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status,
			device_info, metadata, owner_id, last_seen_at
		)
		VALUES ($1, $2, 'attacker-runtime', 'local', $3, 'online', '', '{}'::jsonb, $4, now())
		RETURNING id
	`, testWorkspaceID, daemonID, attackerProvider, attackerID).Scan(&attackerRuntimeID); err != nil {
		t.Fatalf("seed mixed-owner runtime: %v", err)
	}

	deleteW := httptest.NewRecorder()
	testHandler.DeleteComputer(deleteW, withURLParam(newRequestAs(attackerID, http.MethodDelete, "/api/computers/"+daemonID, nil), "daemonId", daemonID))
	if deleteW.Code != http.StatusForbidden {
		t.Fatalf("DeleteComputer mixed-owner group: expected 403, got %d: %s", deleteW.Code, deleteW.Body.String())
	}

	var remainingRuntimeCount int
	if err := testPool.QueryRow(ctx, `
		SELECT COUNT(*) FROM agent_runtime
		WHERE id = $1 OR id = $2
	`, victimRuntimeID, attackerRuntimeID).Scan(&remainingRuntimeCount); err != nil {
		t.Fatalf("count remaining runtimes: %v", err)
	}
	if remainingRuntimeCount != 2 {
		t.Fatalf("expected both runtimes to remain after denied delete, got %d", remainingRuntimeCount)
	}

	var revoked bool
	if err := testPool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM daemon_token WHERE token_hash = $1`, tokenHash).Scan(&revoked); err != nil {
		t.Fatalf("read daemon token revoked_at: %v", err)
	}
	if revoked {
		t.Fatal("denied delete revoked the victim daemon token")
	}
}
