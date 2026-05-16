package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ComputerResponse is the §6.2 aggregate view of a daemon. It's the
// (workspace_id, daemon_id) group rollup of agent_runtime rows; no new
// table backs it. computer_id == daemon_id (§6.1 / D1) — the URL
// /computers/<daemon_id> is the canonical identifier the UI uses.
type ComputerResponse struct {
	ID            string                 `json:"id"`
	WorkspaceID   string                 `json:"workspace_id"`
	Name          string                 `json:"name"`
	Kind          string                 `json:"kind"`
	DeviceInfo    string                 `json:"device_info"`
	InstallSource string                 `json:"install_source"`
	Metadata      map[string]any         `json:"metadata"`
	OwnerID       *string                `json:"owner_id"`
	Status        string                 `json:"status"`
	LastSeenAt    *string                `json:"last_seen_at"`
	CreatedAt     string                 `json:"created_at"`
	Runtimes      []AgentRuntimeResponse `json:"runtimes"`
	RuntimeCount  int                    `json:"runtime_count"`
}

// computerListItem trims the per-runtime detail off the list response so
// /api/computers stays cheap even on workspaces with many daemons. The UI
// fetches the full /api/computers/{id} detail when a row is selected.
type computerListItem struct {
	ID            string         `json:"id"`
	WorkspaceID   string         `json:"workspace_id"`
	Name          string         `json:"name"`
	Kind          string         `json:"kind"`
	DeviceInfo    string         `json:"device_info"`
	InstallSource string         `json:"install_source"`
	Metadata      map[string]any `json:"metadata"`
	OwnerID       *string        `json:"owner_id"`
	Status        string         `json:"status"`
	LastSeenAt    *string        `json:"last_seen_at"`
	CreatedAt     string         `json:"created_at"`
	RuntimeCount  int            `json:"runtime_count"`
}

// ListComputers groups every agent_runtime in the workspace by daemon_id
// and returns one aggregate row per Computer. agent_runtime rows without
// a daemon_id (legacy cloud / pre-pairing data) are skipped — there is
// no Computer to attach them to.
func (h *Handler) ListComputers(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !ok {
		return
	}

	runtimes, err := h.Queries.ListAgentRuntimes(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list computers")
		return
	}

	groups := groupRuntimesByDaemon(runtimes)
	resp := make([]computerListItem, 0, len(groups))
	for _, g := range groups {
		c := buildComputer(g)
		resp = append(resp, computerListItem{
			ID:            c.ID,
			WorkspaceID:   c.WorkspaceID,
			Name:          c.Name,
			Kind:          c.Kind,
			DeviceInfo:    c.DeviceInfo,
			InstallSource: c.InstallSource,
			Metadata:      c.Metadata,
			OwnerID:       c.OwnerID,
			Status:        c.Status,
			LastSeenAt:    c.LastSeenAt,
			CreatedAt:     c.CreatedAt,
			RuntimeCount:  c.RuntimeCount,
		})
	}

	// Sort by name for a stable rendering order. The UI re-sorts client-side
	// based on user preferences, but a deterministic server order keeps tests
	// and snapshot diffs sane.
	sort.Slice(resp, func(i, j int) bool { return resp[i].Name < resp[j].Name })

	writeJSON(w, http.StatusOK, resp)
}

// GetComputer returns the §6.2 detail view: the same aggregate fields as
// the list plus the full runtimes[] array. Lookup is by daemon_id within
// the caller's workspace — there is no global UUID for a Computer.
func (h *Handler) GetComputer(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !ok {
		return
	}
	daemonID := chi.URLParam(r, "daemonId")
	if daemonID == "" {
		writeError(w, http.StatusBadRequest, "daemon_id is required")
		return
	}

	runtimes, err := h.Queries.ListAgentRuntimes(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load computer")
		return
	}

	var group []db.AgentRuntime
	for _, rt := range runtimes {
		if rt.DaemonID.Valid && rt.DaemonID.String == daemonID {
			group = append(group, rt)
		}
	}
	if len(group) == 0 {
		writeError(w, http.StatusNotFound, "computer not found")
		return
	}

	writeJSON(w, http.StatusOK, buildComputer(group))
}

// DeleteComputer implements §6.3: daemon-scoped Remove. Removes every
// agent_runtime row for this (workspace, daemon) pair and revokes the
// daemon_token in this workspace only — the daemon's bindings to other
// workspaces stay intact (the daemon process itself is never uninstalled).
//
// D2 contract: if any agent_runtime under this daemon still has active
// agents or running tasks, return 409 with the occupants so the UI can
// guide the user to cancel / unbind first.
func (h *Handler) DeleteComputer(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found")
	if !ok {
		return
	}
	daemonID := chi.URLParam(r, "daemonId")
	if daemonID == "" {
		writeError(w, http.StatusBadRequest, "daemon_id is required")
		return
	}

	runtimes, err := h.Queries.ListAgentRuntimes(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load computer")
		return
	}

	var group []db.AgentRuntime
	for _, rt := range runtimes {
		if rt.DaemonID.Valid && rt.DaemonID.String == daemonID {
			group = append(group, rt)
		}
	}
	if len(group) == 0 {
		writeError(w, http.StatusNotFound, "computer not found")
		return
	}

	// Permission gate: every runtime under this daemon must be editable by
	// the caller. A daemon-scoped delete removes the whole group, so a mixed
	// owner group must not be removable just because the caller owns one row.
	for _, rt := range group {
		if !canEditRuntime(member, rt) {
			writeError(w, http.StatusForbidden, "you can only remove computers you own")
			return
		}
	}

	// D2 (§6.3): block delete when any runtime under this daemon is still
	// occupied — either by an unarchived agent or a non-terminal task. The
	// response surfaces both id lists so the UI can route the user to "See
	// agents" / "See tasks" before the Remove button re-enables. Counting
	// only active agents (the prior behaviour) would let a delete succeed
	// while queued/dispatched/running tasks are still pointed at this
	// runtime, which then cascade-delete via the agent_task_queue FK and
	// either 500 (race) or silently destroy history (lose).
	var activeAgentIDs []string
	var activeTaskIDs []string
	for _, rt := range group {
		agentIDs, err := h.Queries.ListActiveAgentsByRuntime(r.Context(), rt.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to check computer dependencies")
			return
		}
		for _, id := range agentIDs {
			activeAgentIDs = append(activeAgentIDs, uuidToString(id))
		}
		taskIDs, err := h.Queries.ListActiveTasksByRuntime(r.Context(), rt.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to check computer dependencies")
			return
		}
		for _, id := range taskIDs {
			activeTaskIDs = append(activeTaskIDs, uuidToString(id))
		}
	}
	if len(activeAgentIDs) > 0 || len(activeTaskIDs) > 0 {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":         "computer is still in use. Cancel running tasks and unbind agents before removing it.",
			"active_agents": activeAgentIDs,
			"active_tasks":  activeTaskIDs,
		})
		return
	}

	// D4: delete runtime rows and revoke the daemon_token in the same DB
	// transaction. Revoke is the kill switch — if it doesn't land, the
	// daemon still holds a usable mdt_ and could re-register the Computer
	// back into existence after the rows are gone. Token cache invalidation
	// runs only after commit (a successful revoke that the cache doesn't
	// see yet is fine; an invalidated cache for a failed revoke would
	// briefly mask a still-valid token).
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		slog.Error("delete computer: begin tx failed", "error", err, "daemon_id", daemonID)
		writeError(w, http.StatusInternalServerError, "failed to delete computer")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	for _, rt := range group {
		if err := qtx.DeleteArchivedAgentsByRuntime(r.Context(), rt.ID); err != nil {
			slog.Error("delete computer: clean archived agents failed", "error", err, "runtime_id", uuidToString(rt.ID))
			writeError(w, http.StatusInternalServerError, "failed to clean up archived agents")
			return
		}
		if err := qtx.DeleteAgentRuntime(r.Context(), rt.ID); err != nil {
			slog.Error("delete computer: delete runtime failed", "error", err, "runtime_id", uuidToString(rt.ID))
			writeError(w, http.StatusInternalServerError, "failed to delete computer")
			return
		}
	}

	revoked, err := qtx.RevokeDaemonTokensByWorkspaceAndDaemon(r.Context(), db.RevokeDaemonTokensByWorkspaceAndDaemonParams{
		WorkspaceID: parseUUID(workspaceID),
		DaemonID:    daemonID,
	})
	if err != nil {
		// Hard failure: do not commit. Otherwise the runtime rows would be
		// gone but the daemon's mdt_ would survive, contradicting D4 ("kill
		// switch on credential") and letting the daemon silently re-register.
		slog.Error("delete computer: revoke daemon tokens failed, rolling back", "error", err, "daemon_id", daemonID)
		writeError(w, http.StatusInternalServerError, "failed to revoke daemon credentials")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		slog.Error("delete computer: commit failed", "error", err, "daemon_id", daemonID)
		writeError(w, http.StatusInternalServerError, "failed to delete computer")
		return
	}

	// Cache invalidation must run AFTER commit. Running it before would
	// briefly mask the still-valid token if the tx rolled back; running it
	// after means the worst case is a small window where a cached identity
	// outlives the revoked DB row, which the next cache miss resolves.
	for _, hash := range revoked {
		h.DaemonTokenCache.Invalidate(r.Context(), hash)
	}

	userID := uuidToString(member.UserID)
	slog.Info(
		"computer removed",
		"workspace_id", workspaceID,
		"daemon_id", daemonID,
		"runtimes_removed", len(group),
		"tokens_revoked", len(revoked),
		"removed_by", userID,
	)

	// Reuse the existing daemon-register event channel so the frontend's
	// runtime-list query (and the new computer-list query, once wired up)
	// both refresh without us introducing a new event type the desktop app
	// would need a build to learn about.
	h.publish(protocol.EventDaemonRegister, workspaceID, "member", userID, map[string]any{
		"action": "delete",
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// groupRuntimesByDaemon bins agent_runtime rows by daemon_id, preserving
// the source order within each bin (sqlc returns rows ordered by created_at
// asc, which gives the §6.2 list a stable shape). Runtimes without a
// daemon_id (legacy data) are dropped — they don't belong to any Computer.
func groupRuntimesByDaemon(runtimes []db.AgentRuntime) [][]db.AgentRuntime {
	index := map[string]int{}
	var groups [][]db.AgentRuntime
	for _, rt := range runtimes {
		if !rt.DaemonID.Valid || rt.DaemonID.String == "" {
			continue
		}
		if i, ok := index[rt.DaemonID.String]; ok {
			groups[i] = append(groups[i], rt)
			continue
		}
		index[rt.DaemonID.String] = len(groups)
		groups = append(groups, []db.AgentRuntime{rt})
	}
	return groups
}

// buildComputer collapses a group of agent_runtime rows into a single
// ComputerResponse per the §6.2 / D3 field table. D3 explicitly forbids
// new columns: every field below is either taken from an existing column,
// derived from one (kind ← runtime_mode), or pulled from metadata jsonb.
func buildComputer(group []db.AgentRuntime) ComputerResponse {
	first := group[0]

	// Status: §6.2 rule — any row whose status is "online" makes the
	// Computer online. The Redis-TTL-aware liveness check the RFC describes
	// runs in the daemon heartbeat path; agent_runtime.status is the
	// already-resolved view of that, so we trust it here.
	status := "offline"
	var lastSeen pgtype.Timestamptz
	for _, rt := range group {
		if rt.Status == "online" {
			status = "online"
		}
		if rt.LastSeenAt.Valid {
			if !lastSeen.Valid || rt.LastSeenAt.Time.After(lastSeen.Time) {
				lastSeen = rt.LastSeenAt
			}
		}
	}

	metadata := map[string]any{}
	if first.Metadata != nil {
		_ = json.Unmarshal(first.Metadata, &metadata)
	}
	installSource, _ := metadata["install_source"].(string)

	runtimes := make([]AgentRuntimeResponse, len(group))
	for i, rt := range group {
		runtimes[i] = runtimeToResponse(rt)
	}

	return ComputerResponse{
		ID:            first.DaemonID.String,
		WorkspaceID:   uuidToString(first.WorkspaceID),
		Name:          first.Name,
		Kind:          first.RuntimeMode, // D3: kind := runtime_mode
		DeviceInfo:    first.DeviceInfo,
		InstallSource: installSource,
		Metadata:      metadata,
		OwnerID:       uuidToPtr(first.OwnerID),
		Status:        status,
		LastSeenAt:    timestampToPtr(lastSeen),
		CreatedAt:     timestampToString(first.CreatedAt),
		Runtimes:      runtimes,
		RuntimeCount:  len(group),
	}
}
