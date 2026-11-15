package fs

import (
	"path/filepath"
	"sync"
)

// mutationQueue serializes read-modify-write sequences that target the same
// file. Two edits to one file running concurrently would each read the original
// content and the second would clobber the first, so the whole sequence is held
// under a per-path lock. Different files still proceed in parallel.
//
// pi keys its queue on the resolved real path so a symlink and its target share
// one lock. The same is done here, falling back to the lexical path when the
// file does not exist yet (write creating a new file).
type mutationQueue struct {
	mu    sync.Mutex
	locks map[string]*pathLock
}

type pathLock struct {
	mu   sync.Mutex
	refs int
}

func newMutationQueue() *mutationQueue {
	return &mutationQueue{locks: make(map[string]*pathLock)}
}

// with runs fn while holding the lock for path.
func (q *mutationQueue) with(path string, fn func() error) error {
	key := q.key(path)

	q.mu.Lock()
	lock := q.locks[key]
	if lock == nil {
		lock = &pathLock{}
		q.locks[key] = lock
	}
	lock.refs++
	q.mu.Unlock()

	lock.mu.Lock()
	err := fn()
	lock.mu.Unlock()

	q.mu.Lock()
	lock.refs--
	if lock.refs == 0 {
		delete(q.locks, key)
	}
	q.mu.Unlock()
	return err
}

// key prefers the fully resolved path so every alias of a file maps to one lock.
func (q *mutationQueue) key(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = path
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		return resolved
	}
	return absolute
}
