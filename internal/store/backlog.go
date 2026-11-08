package store

import (
	"context"
	"database/sql"
	"time"
)

// BacklogTask is a task the queue held in SQLite before the order streams,
// which the API moves into them.
type BacklogTask struct {
	ID       int64
	Kind     string
	Hash     Hash
	Priority int
	// Attempts is how many attempts at it failed.
	Attempts int
	// NotBefore is when it is due; zero for at once.
	NotBefore time.Time
}

// Backlog returns up to n tasks of the backlog, by priority and then in the
// order they were queued; none once the backlog is gone.
func (s *Store) Backlog(ctx context.Context, n int) ([]BacklogTask, error) {
	ok, err := s.hasBacklog(ctx)
	if err != nil || !ok {
		return nil, err
	}
	var out []BacklogTask
	err = s.each(ctx, func(rows *sql.Rows) error {
		var t BacklogTask
		var b []byte
		var notBefore int64
		if err := rows.Scan(&t.ID, &t.Kind, &b, &t.Priority, &t.Attempts, &notBefore); err != nil {
			return err
		}
		t.Hash = Hash(b)
		if notBefore > 0 {
			t.NotBefore = time.UnixMilli(notBefore)
		}
		out = append(out, t)
		return nil
	}, "SELECT id, kind, infohash, priority, attempts, not_before FROM task_backlog ORDER BY priority, id LIMIT ?", n)
	return out, err
}

// DropBacklog deletes the tasks of the IDs from the backlog, and the backlog
// once it holds none.
func (s *Store) DropBacklog(ctx context.Context, ids []int64) error {
	ok, err := s.hasBacklog(ctx)
	if err != nil || !ok {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if len(ids) > 0 {
			if _, err := tx.ExecContext(ctx, "DELETE FROM task_backlog WHERE id IN ("+placeholders(len(ids))+")", int64Args(ids)...); err != nil {
				return err
			}
		}
		var left bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM task_backlog)").Scan(&left); err != nil || left {
			return err
		}
		_, err := tx.ExecContext(ctx, "DROP TABLE task_backlog")
		return err
	})
}

// hasBacklog reports whether the backlog table is there.
func (s *Store) hasBacklog(ctx context.Context) (bool, error) {
	var name string
	err := s.r.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'task_backlog'").Scan(&name)
	if err = ignoreNoRows(err); err != nil {
		return false, err
	}
	return name != "", nil
}
