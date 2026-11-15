package app

import (
	"errors"

	"pi-go/internal/session"
)

// Archived reports whether the session in effect is archived. An archived
// conversation is still on disk and still readable — /recover opens it — but
// nothing may be written to it, and the command layer allows only /restore and
// /delete while it is the one on screen.
func (a *App) Archived() bool {
	return session.Archived(a.manager.File())
}

// Archive hides the conversation in effect from the lists that open
// conversations, and starts a fresh one in the same directory: archiving is
// how a conversation is put away finished, so the prompt has to land somewhere
// else. The file and its messages stay exactly as they were.
//
// Only a conversation with a file can be archived, because the mark is a file
// beside it. Asking to archive something nobody talked to is an error rather
// than a silent no-op: the user thinks a conversation is on disk, and it is not.
func (a *App) Archive() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return ErrBusy
	}
	if err := session.Archive(a.manager.File()); err != nil {
		if errors.Is(err, session.ErrNoSessionFile) {
			return ErrNothingToArchive
		}
		return err
	}
	return a.newSessionLocked()
}

// Restore lifts the mark, and with it every restriction Archive imposed.
func (a *App) Restore() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	file := a.manager.File()
	if !session.Archived(file) {
		return ErrNotArchived
	}
	return session.Unarchive(file)
}

// Delete removes an archived conversation from disk and starts a fresh one:
// the file is gone, so there is nothing left to stand in.
//
// Deletion is refused for a live session on purpose. The archive is the second
// look the user asked for before destroying a transcript, and a command that
// deletes whatever is on screen would be the only thing in atlas that destroys
// history without one.
func (a *App) Delete() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return ErrBusy
	}
	file := a.manager.File()
	if !session.Archived(file) {
		return ErrNotArchived
	}
	if err := session.DeleteFile(file); err != nil {
		return err
	}
	return a.newSessionLocked()
}

// guardArchived is the write barrier for an archived conversation. Every method
// that would append to the session file, or replace it, goes through it: the
// command layer already refuses those commands, but a second caller — the RPC
// server, a test, a future front end — must not be able to reach the file
// behind that policy's back.
func (a *App) guardArchived() error {
	if a.Archived() {
		return ErrArchived
	}
	return nil
}
