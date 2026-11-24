package webhost

import (
	"pi-go/internal/httpapi"
	"pi-go/internal/trajectory"
)

// Trajectory folds the conversation in view into the rows a debugger walks.
//
// The fold is the kernel's, for the reason the transcript's is: one package owns
// what counts as a fact and in which order, so the window cannot drift from the
// file it is reading. The entries are the manager's own log rather than a second
// read of the file — the manager appends as it goes, so this answers for a
// conversation that has not been persisted yet as well as for one on disk.
//
// The leaf is the manager's current one, which is what "the branch in view"
// means. A session with no leaf folds to nothing: the window then shows an empty
// view instead of a plausible path the kernel never took.
func (h *Host) Trajectory() ([]httpapi.TrajectoryRow, error) {
	manager := h.activeSlot().live.App.Session()
	rows := trajectory.Fold(manager.Entries(), manager.LeafID())
	out := make([]httpapi.TrajectoryRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, httpapi.TrajectoryRow{
			Index:        row.Index,
			Seq:          row.Seq,
			EntryID:      row.EntryID,
			ParentID:     row.ParentID,
			Turn:         row.Turn,
			Kind:         string(row.Kind),
			Timestamp:    row.Timestamp,
			DurationMs:   row.DurationMs,
			IsError:      row.IsError,
			Interrupted:  row.Interrupted,
			Tool:         row.Tool,
			Ref:          row.Ref,
			Text:         row.Text,
			Thinking:     row.Thinking,
			Error:        row.Error,
			Args:         row.Args,
			Result:       row.Result,
			Model:        row.Model,
			Usage:        row.Usage,
			TokensBefore: row.TokensBefore,
		})
	}
	return out, nil
}
