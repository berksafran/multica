package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/daemon/repocache"
)

func TestHealthHandlerReportsCLIVersionAndActiveTaskCount(t *testing.T) {
	t.Parallel()

	d := &Daemon{
		cfg: Config{
			CLIVersion:    "v9.9.9",
			DaemonID:      "daemon-test",
			DeviceName:    "dev",
			ServerBaseURL: "http://localhost:8080",
		},
		workspaces: map[string]*workspaceState{},
		logger:     slog.Default(),
	}
	d.activeTasks.Store(3)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	d.healthHandler(time.Now()).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// Decode into a raw map so the test locks in the exact wire-level JSON
	// keys — the desktop TS client depends on snake_case (cli_version,
	// active_task_count), so a silent struct-tag rename must fail here.
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw response: %v", err)
	}
	if got, want := raw["cli_version"], "v9.9.9"; got != want {
		t.Errorf("cli_version key: got %v, want %q", got, want)
	}
	// JSON numbers decode to float64 through map[string]any.
	if got, want := raw["active_task_count"], float64(3); got != want {
		t.Errorf("active_task_count key: got %v, want %v", got, want)
	}
	if got, want := raw["status"], "running"; got != want {
		t.Errorf("status key: got %v, want %q", got, want)
	}

	// Also round-trip into the typed struct as a separate check that the
	// field values match, independent of key naming.
	var resp HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode typed response: %v", err)
	}
	if resp.CLIVersion != "v9.9.9" {
		t.Errorf("CLIVersion: got %q, want %q", resp.CLIVersion, "v9.9.9")
	}
	if resp.ActiveTaskCount != 3 {
		t.Errorf("ActiveTaskCount: got %d, want 3", resp.ActiveTaskCount)
	}
}

func TestHealthHandlerActiveTaskCountTracksCounter(t *testing.T) {
	t.Parallel()

	d := &Daemon{
		cfg:        Config{CLIVersion: "v1.0.0"},
		workspaces: map[string]*workspaceState{},
		logger:     slog.Default(),
	}
	handler := d.healthHandler(time.Now())

	// Simulate the pollLoop increment/decrement protocol.
	d.activeTasks.Add(1)
	d.activeTasks.Add(1)
	assertActiveTaskCount(t, handler, 2)

	d.activeTasks.Add(-1)
	assertActiveTaskCount(t, handler, 1)

	d.activeTasks.Add(-1)
	assertActiveTaskCount(t, handler, 0)
}

func TestShutdownHandlerPostCancelsDaemonContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &Daemon{cancelFunc: cancel}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/shutdown", nil)
	d.shutdownHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("daemon context was not cancelled after POST /shutdown")
	}
}

func TestShutdownHandlerRejectsNonPost(t *testing.T) {
	t.Parallel()

	cancelled := false
	d := &Daemon{cancelFunc: func() { cancelled = true }}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/shutdown", nil)
	d.shutdownHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
	// Give the handler's deferred cancel goroutine a moment to fire
	// in case a bug causes it to run anyway.
	time.Sleep(10 * time.Millisecond)
	if cancelled {
		t.Fatal("GET request should not trigger cancellation")
	}
}

func TestWorkspaceSyncHandlerReloadsCredentialsAndRegistersNewWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(stubAgentVersion(t))

	const daemonID = "daemon-sync-test"
	const workspaceID = "ws-new"
	const daemonToken = "mdt_new_workspace"
	var registerCalls int32

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/daemon/register" {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt32(&registerCalls, 1)
		if got := r.Header.Get("Authorization"); got != "Bearer "+daemonToken {
			t.Errorf("Authorization = %q, want bearer daemon token", got)
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode register request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if body["workspace_id"] != workspaceID {
			t.Errorf("workspace_id = %v, want %s", body["workspace_id"], workspaceID)
			http.Error(w, "bad workspace", http.StatusBadRequest)
			return
		}
		if body["daemon_id"] != daemonID {
			t.Errorf("daemon_id = %v, want %s", body["daemon_id"], daemonID)
			http.Error(w, "bad daemon", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(RegisterResponse{
			Runtimes:     []Runtime{{ID: "rt-new", Name: "Claude", Provider: "claude", Status: "online"}},
			ReposVersion: "v1",
		})
	}))
	t.Cleanup(api.Close)

	if err := cli.SaveDaemonCredentials(cli.DaemonCredentialStore{
		Version: 1,
		Credentials: []cli.DaemonCredential{{
			ServerURL:   api.URL,
			WorkspaceID: workspaceID,
			DaemonID:    daemonID,
			DaemonToken: daemonToken,
			IssuedAt:    time.Now().UTC().Format(time.RFC3339),
		}},
	}, ""); err != nil {
		t.Fatalf("save daemon credentials: %v", err)
	}

	d := New(Config{
		ServerBaseURL:  api.URL,
		DaemonID:       daemonID,
		DeviceName:     "dev",
		CLIVersion:     "test",
		Agents:         map[string]AgentEntry{"claude": {Path: "claude"}},
		WorkspacesRoot: t.TempDir(),
	}, slog.Default())

	rec := httptest.NewRecorder()
	d.workspaceSyncHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/workspaces/sync", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("sync handler: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := atomic.LoadInt32(&registerCalls); got != 1 {
		t.Fatalf("register calls = %d, want 1", got)
	}
	if got := d.tokenForCtx(WithCallWorkspaceID(context.Background(), workspaceID)); got != daemonToken {
		t.Fatalf("workspace token = %q, want saved daemon token", got)
	}
	d.mu.Lock()
	ws := d.workspaces[workspaceID]
	d.mu.Unlock()
	if ws == nil || len(ws.runtimeIDs) != 1 || ws.runtimeIDs[0] != "rt-new" {
		t.Fatalf("workspace state = %#v, want registered runtime rt-new", ws)
	}
}

func TestWorkspaceSyncHandlerPreservesPATWorkspacesWhenAddingDaemonTokenWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(stubAgentVersion(t))

	const (
		daemonID     = "daemon-mixed-sync-test"
		oldWorkspace = "ws-old"
		newWorkspace = "ws-new"
		patToken     = "mul_legacy_pat"
		daemonToken  = "mdt_new_workspace"
	)
	var listCalls int32
	var registerCalls int32

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/workspaces":
			atomic.AddInt32(&listCalls, 1)
			if got := r.Header.Get("Authorization"); got != "Bearer "+patToken {
				t.Errorf("ListWorkspaces Authorization = %q, want PAT", got)
				http.Error(w, "bad auth", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]WorkspaceInfo{{ID: oldWorkspace, Name: "Old Workspace"}})
		case "/api/daemon/register":
			atomic.AddInt32(&registerCalls, 1)
			if got := r.Header.Get("Authorization"); got != "Bearer "+daemonToken {
				t.Errorf("Register Authorization = %q, want daemon token", got)
				http.Error(w, "bad auth", http.StatusUnauthorized)
				return
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode register request: %v", err)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if body["workspace_id"] != newWorkspace {
				t.Errorf("workspace_id = %v, want %s", body["workspace_id"], newWorkspace)
				http.Error(w, "bad workspace", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(RegisterResponse{
				Runtimes:     []Runtime{{ID: "rt-new", Name: "Claude", Provider: "claude", Status: "online"}},
				ReposVersion: "v1",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)

	if err := cli.SaveCLIConfig(cli.CLIConfig{ServerURL: api.URL, Token: patToken}); err != nil {
		t.Fatalf("save CLI config: %v", err)
	}
	if err := cli.SaveDaemonCredentials(cli.DaemonCredentialStore{
		Version: 1,
		Credentials: []cli.DaemonCredential{{
			ServerURL:   api.URL,
			WorkspaceID: newWorkspace,
			DaemonID:    daemonID,
			DaemonToken: daemonToken,
			IssuedAt:    time.Now().UTC().Format(time.RFC3339),
		}},
	}, ""); err != nil {
		t.Fatalf("save daemon credentials: %v", err)
	}

	d := New(Config{
		ServerBaseURL:  api.URL,
		DaemonID:       daemonID,
		DeviceName:     "dev",
		CLIVersion:     "test",
		Agents:         map[string]AgentEntry{"claude": {Path: "claude"}},
		WorkspacesRoot: t.TempDir(),
	}, slog.Default())
	oldState := newWorkspaceState(oldWorkspace, []string{"rt-old"}, "v0", nil, nil)
	d.workspaces[oldWorkspace] = oldState
	d.runtimeIndex["rt-old"] = Runtime{ID: "rt-old", Provider: "claude", Status: "online"}
	d.runtimeWorkspace["rt-old"] = oldWorkspace

	rec := httptest.NewRecorder()
	d.workspaceSyncHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/workspaces/sync", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("sync handler: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := atomic.LoadInt32(&listCalls); got != 1 {
		t.Fatalf("ListWorkspaces calls = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&registerCalls); got != 1 {
		t.Fatalf("register calls = %d, want 1", got)
	}
	if got := d.tokenForCtx(WithCallWorkspaceID(context.Background(), oldWorkspace)); got != patToken {
		t.Fatalf("old workspace token = %q, want PAT", got)
	}
	if got := d.tokenForCtx(WithCallWorkspaceID(context.Background(), newWorkspace)); got != daemonToken {
		t.Fatalf("new workspace token = %q, want daemon token", got)
	}

	d.mu.Lock()
	gotOld := d.workspaces[oldWorkspace]
	gotNew := d.workspaces[newWorkspace]
	d.mu.Unlock()
	if gotOld != oldState {
		t.Fatalf("old PAT workspace state was replaced or removed: got %#v want original", gotOld)
	}
	if gotNew == nil || len(gotNew.runtimeIDs) != 1 || gotNew.runtimeIDs[0] != "rt-new" {
		t.Fatalf("new workspace state = %#v, want registered runtime rt-new", gotNew)
	}
}

func TestHealthHandlerRespondsWhileTaskRepoLookupWaits(t *testing.T) {
	const workspaceID = "ws-health"
	const repoURL = "https://github.com/org/repo.git"
	cache := newBlockingLookupRepoCache("/cache/org/repo.git")
	d := &Daemon{
		cfg: Config{CLIVersion: "v1.0.0"},
		workspaces: map[string]*workspaceState{
			workspaceID: {
				workspaceID:     workspaceID,
				runtimeIDs:      []string{"rt-1"},
				allowedRepoURLs: map[string]struct{}{repoURL: {}},
				taskRepoURLs:    map[string]struct{}{},
			},
		},
		repoCache: cache,
		logger:    slog.Default(),
	}
	defer cache.release()

	registerDone := make(chan struct{})
	go func() {
		d.registerTaskRepos(workspaceID, []RepoData{{URL: repoURL}})
		close(registerDone)
	}()
	cache.waitForLookup(t)

	rec := httptest.NewRecorder()
	healthDone := make(chan struct{})
	go func() {
		d.healthHandler(time.Now()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		close(healthDone)
	}()

	select {
	case <-healthDone:
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	case <-time.After(time.Second):
		t.Fatal("/health blocked behind task repo cache lookup")
	}

	cache.release()
	select {
	case <-registerDone:
	case <-time.After(time.Second):
		t.Fatal("registerTaskRepos did not unblock after repo lookup finished")
	}
}

type blockingLookupRepoCache struct {
	path          string
	lookupSeen    chan struct{}
	releaseLookup chan struct{}
	releaseOnce   sync.Once
}

func newBlockingLookupRepoCache(path string) *blockingLookupRepoCache {
	return &blockingLookupRepoCache{
		path:          path,
		lookupSeen:    make(chan struct{}),
		releaseLookup: make(chan struct{}),
	}
}

func (c *blockingLookupRepoCache) Lookup(_, _ string) string {
	select {
	case <-c.lookupSeen:
	default:
		close(c.lookupSeen)
	}
	<-c.releaseLookup
	return c.path
}

func (c *blockingLookupRepoCache) Sync(string, []repocache.RepoInfo) error {
	return nil
}

func (c *blockingLookupRepoCache) CreateWorktree(repocache.WorktreeParams) (*repocache.WorktreeResult, error) {
	return nil, nil
}

func (c *blockingLookupRepoCache) waitForLookup(t *testing.T) {
	t.Helper()
	select {
	case <-c.lookupSeen:
	case <-time.After(time.Second):
		t.Fatal("registerTaskRepos did not call repo lookup")
	}
}

func (c *blockingLookupRepoCache) release() {
	c.releaseOnce.Do(func() {
		close(c.releaseLookup)
	})
}

func assertActiveTaskCount(t *testing.T, h http.HandlerFunc, want int64) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	var resp HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ActiveTaskCount != want {
		t.Errorf("active_task_count: got %d, want %d", resp.ActiveTaskCount, want)
	}
}
