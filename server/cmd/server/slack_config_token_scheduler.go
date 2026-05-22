package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/handler"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// slack_config_token_scheduler keeps the singleton Slack app-config token
// fresh by triggering a rotation before its 12-hour TTL expires. Without
// this, every manifest-API call (create/update/verify/delete) starts
// returning `token_expired` 12h after the deployment last touched the
// token, and the admin sees the same red error we built this PR to solve.
//
// The handler-side configTokenService.Current() also rotates inline when
// a request finds the token near expiry — this scheduler is the
// belt-and-suspenders layer that catches deployments where nobody calls
// the manifest API for a day or two and would otherwise sleep past the
// rotation window.

const (
	// slackConfigTokenTick is how often we wake up to check expiry. The
	// rotation window itself is configured on the service side
	// (rotationLeadTime = 1h before expiry); we tick more frequently so
	// a transient Slack outage gets at least a few retries before the
	// remaining buffer runs out.
	slackConfigTokenTick = 5 * time.Minute

	// slackConfigTokenLead matches handler.rotationLeadTime: we don't
	// want to re-read it across packages, but a too-short value here
	// would let an expired token slip through. Keep generously below
	// the 12h Slack TTL.
	slackConfigTokenLead = 60 * time.Minute
)

// runSlackConfigTokenScheduler is the background loop. It exits when ctx
// is cancelled. Safe to launch with `go` from main.
func runSlackConfigTokenScheduler(ctx context.Context, h *handler.Handler, queries *db.Queries) {
	// Initial kick: if a deployment boots near expiry, don't wait a full
	// 5 minutes for the first tick.
	tickSlackConfigTokenRotation(ctx, h, queries)

	ticker := time.NewTicker(slackConfigTokenTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickSlackConfigTokenRotation(ctx, h, queries)
		}
	}
}

// tickSlackConfigTokenRotation does the work for one iteration. Pulled
// out for testability and so the initial kick + ticker share one code
// path.
func tickSlackConfigTokenRotation(ctx context.Context, h *handler.Handler, queries *db.Queries) {
	row, err := queries.GetSlackConfigToken(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		// Never bootstrapped. If the env var is set, the deployment is
		// still on the legacy path — log periodically so the operator
		// sees a nudge to paste tokens via the admin UI.
		if strings.TrimSpace(os.Getenv("SLACK_CONFIG_TOKEN")) != "" {
			slog.Debug("slack config token scheduler: env-var fallback in use; paste tokens in admin UI to enable auto-rotation")
		}
		return
	}
	if err != nil {
		slog.Warn("slack config token scheduler: load failed", "err", err)
		return
	}

	if time.Now().Add(slackConfigTokenLead).Before(row.ExpiresAt.Time) {
		// Plenty of life left.
		return
	}

	// The handler service owns the actual rotation (mutex, persistence,
	// error-recording). Reuse it so a manual Rotate-now button press and
	// a scheduler tick share the same critical section.
	svc, err := h.SlackConfigTokensForScheduler()
	if err != nil {
		slog.Warn("slack config token scheduler: service unavailable", "err", err)
		return
	}
	if err := svc.Rotate(ctx); err != nil {
		slog.Warn("slack config token scheduler: rotate failed", "err", err)
		return
	}
	slog.Info("slack config token scheduler: rotated successfully")
}
