package api

import (
	"context"
	"errors"
	"strings"

	"github.com/Thrapis/bellingua/internal/model"
	"github.com/Thrapis/bellingua/internal/store"
)

// revert_sweep: undo a replace_in_targets run. Every real run writes its
// history rows under one batch id (the sweepId it returns); this puts back
// the old text of each unit that still holds exactly what the sweep wrote.

type revertIn struct {
	Project int64  `json:"project" jsonschema:"project id"`
	SweepID string `json:"sweepId" jsonschema:"the sweepId returned by a real (dryRun=false) replace_in_targets run"`
	DryRun  *bool  `json:"dryRun,omitempty" jsonschema:"default true: report what would be restored and write nothing; pass false to write"`
}

type mcpRevert struct {
	DryRun  bool   `json:"dryRun"`
	SweepID string `json:"sweepId"`
	Matched int    `json:"matched"` // units the sweep changed
	// Reverted units were put back (or, in a dry run, would be).
	Reverted int `json:"reverted"`
	// Skipped units were not touched: edited again since the sweep, or approved since.
	Skipped  int               `json:"skipped"`
	Failed   int               `json:"failed"`
	Samples  []mcpSweepSample  `json:"samples"` // before = the text now, after = the text restored
	Failures []mcpSweepFailure `json:"failures"`
}

func (s *Server) revertSweep(ctx context.Context, in revertIn) (mcpRevert, error) {
	out := mcpRevert{DryRun: in.DryRun == nil || *in.DryRun, SweepID: in.SweepID, Samples: []mcpSweepSample{}, Failures: []mcpSweepFailure{}}
	if !strings.HasPrefix(in.SweepID, "sweep-") {
		return out, badRequest("sweepId must be the sweepId of a replace_in_targets run (it starts with \"sweep-\")")
	}
	if _, err := s.st.Project(ctx, in.Project); err != nil {
		return out, err
	}
	changes, err := s.st.BatchChanges(ctx, in.Project, in.SweepID)
	if err != nil {
		return out, err
	}
	if len(changes) == 0 {
		return out, badRequest("no changes recorded for that sweep in this project")
	}
	// A unit changed twice in the batch goes back to its first old text.
	type span struct{ old, new *string }
	spans := map[int64]*span{}
	var order []int64
	for _, c := range changes {
		sp, ok := spans[c.UnitID]
		if !ok {
			sp = &span{old: c.Old}
			spans[c.UnitID] = sp
			order = append(order, c.UnitID)
		}
		sp.new = c.New
	}
	out.Matched = len(order)

	batch := store.NewBatch("revert")
	for _, id := range order {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		sp := spans[id]
		u, err := s.st.Unit(ctx, id, false)
		if err != nil {
			return out, err
		}
		// Only a unit that still holds the sweep's result goes back: a later
		// edit (or a later sweep) stays.
		if sp.old == nil || sp.new == nil || u.Target == nil || model.Plain(u.Target) != *sp.new || u.State >= model.Approved {
			out.Skipped++
			continue
		}
		cur := model.Plain(u.Target)
		var saveErr error
		if _, err := model.SplitTarget(*sp.old, u.Source); err != nil {
			saveErr = err
		} else if !out.DryRun {
			old := *sp.old
			_, saveErr = s.save(ctx, id, store.Save{Target: &old, State: u.State, Origin: mcpOrigin, IfBelow: model.Approved, Batch: batch})
		}
		switch {
		case errors.Is(saveErr, store.ErrSkipped):
			out.Skipped++
		case saveErr != nil:
			out.Failed++
			if len(out.Failures) < maxReplaceFailures {
				out.Failures = append(out.Failures, mcpSweepFailure{ID: id, Error: s.replaceError(saveErr)})
			}
		default:
			out.Reverted++
			if len(out.Samples) < maxReplaceSamples {
				b, a := cutAround(cur, *sp.old)
				out.Samples = append(out.Samples, mcpSweepSample{ID: id, Before: b, After: a})
			}
		}
	}
	return out, nil
}
