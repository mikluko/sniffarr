package api

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"

	"sarak.sh/mikluko/sniffarr/internal/bus"
	"sarak.sh/mikluko/sniffarr/internal/orders"
	"sarak.sh/mikluko/sniffarr/internal/store"
)

// backlogChunk is how many tasks of the backlog one step moves.
const backlogChunk = 1000

// moveBacklog moves the tasks the queue held in SQLite into the order
// streams of queues, a chunk at a time, each keeping its priority, failed
// attempts and due time, and drops the backlog once it is empty. A move cut
// short resumes at the next start; the chunk it was moving is published again
// under the same Nats-Msg-Id, which the stream drops within
// bus.OrdersDuplicates.
func moveBacklog(ctx context.Context, s *store.Store, log *slog.Logger, queues ...*orders.Queue) error {
	moved := 0
	for {
		tasks, err := s.Backlog(ctx, backlogChunk)
		if err != nil {
			return fmt.Errorf("backlog: %w", err)
		}
		byLane := map[bus.Lane][]orders.Item{}
		lanes := map[bus.Lane]*orders.Queue{}
		ids := make([]int64, len(tasks))
		for i, t := range tasks {
			ids[i] = t.ID
			q, l, ok := laneOf(queues, t.Kind, t.Priority)
			if !ok {
				log.WarnContext(ctx, "backlog task without a lane", "kind", t.Kind, "priority", t.Priority, "infohash", hex.EncodeToString(t.Hash[:]))
				continue
			}
			lanes[l] = q
			byLane[l] = append(byLane[l], orders.Item{Hash: t.Hash, Attempt: t.Attempts, At: t.NotBefore,
				ID: "backlog:" + t.Kind + ":" + hex.EncodeToString(t.Hash[:])})
		}
		for l, items := range byLane {
			if _, err := lanes[l].Publish(ctx, l, items); err != nil {
				return fmt.Errorf("backlog: %w", err)
			}
		}
		if err := s.DropBacklog(ctx, ids); err != nil {
			return fmt.Errorf("backlog: %w", err)
		}
		moved += len(tasks)
		if len(tasks) < backlogChunk {
			break
		}
	}
	if moved > 0 {
		log.InfoContext(ctx, "moved the queue into the order streams", "tasks", moved)
	}
	return nil
}

// laneOf returns the queue among queues with a lane of the kind at the
// priority, and the lane.
func laneOf(queues []*orders.Queue, kind string, priority int) (*orders.Queue, bus.Lane, bool) {
	for _, q := range queues {
		if l, ok := q.Lane(kind, priority); ok {
			return q, l, true
		}
	}
	return nil, bus.Lane{}, false
}
