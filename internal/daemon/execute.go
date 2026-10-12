package daemon

import (
	"context"

	"github.com/matthew-reed-holden/homerun/internal/store"
)

func (d *Daemon) execute(ctx context.Context, run *store.Run, p *plan, slot int) {
	defer d.mirror(run.Repo).RemoveWorktree(context.WithoutCancel(ctx), run.Worktree)
	// On shutdown the run stays queued/running so restart recovery closes and re-queues it.
	if d.base.Err() != nil {
		return
	}
	// The run's own ctx may be cancelled; the report must still reach GitHub.
	rctx := context.WithoutCancel(ctx)
	if ctx.Err() != nil {
		d.closeOpen(rctx, run, "cancelled", supersededSummary)
		d.setStatus(run, "cancelled")
		return
	}
	d.closeOpen(rctx, run, "cancelled", "not implemented yet")
	d.setStatus(run, "done")
}
