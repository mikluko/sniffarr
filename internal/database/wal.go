package database

import (
	"context"
	"errors"
	"io/fs"
	"os"

	"go.opentelemetry.io/otel/metric"
)

// journalSizeLimit is the size, in bytes, the WAL file is truncated to once
// a checkpoint has reset it.
const journalSizeLimit = 64 << 20

// CheckpointMode is a mode of PRAGMA wal_checkpoint.
type CheckpointMode string

const (
	// Passive backfills what the readers allow and waits for nobody.
	Passive CheckpointMode = "PASSIVE"
	// Truncate waits for the readers, up to the busy timeout, backfills the
	// whole WAL and truncates it to nothing, holding the write lock meanwhile.
	Truncate CheckpointMode = "TRUNCATE"
)

// Checkpointed is what a Checkpoint did.
type Checkpointed struct {
	// Busy reports that a lock stopped the checkpoint short.
	Busy bool
	// Frames is how many frames the WAL held and Backfilled how many of them
	// are in the database. A completed Truncate reports both as 0: the WAL is
	// reset before they are counted.
	Frames, Backfilled int
}

// Checkpoint runs a wal_checkpoint of mode on the writing connection.
func (db *DB) Checkpoint(ctx context.Context, mode CheckpointMode) (Checkpointed, error) {
	var c Checkpointed
	var busy int
	err := db.W.QueryRowContext(ctx, "PRAGMA wal_checkpoint("+string(mode)+")").Scan(&busy, &c.Frames, &c.Backfilled)
	c.Busy = busy != 0
	return c, err
}

// WALSize returns the size of the WAL file in bytes, 0 when there is none.
func (db *DB) WALSize() (int64, error) {
	fi, err := os.Stat(db.path + "-wal")
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

// observeWAL reports WALSize as sniffarr.db.wal.size.
func (db *DB) observeWAL(m metric.Meter) error {
	size, err := m.Int64ObservableGauge("sniffarr.db.wal.size", metric.WithUnit("By"), metric.WithDescription("Size of the write-ahead log."))
	if err != nil {
		return err
	}
	reg, err := m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		n, err := db.WALSize()
		if err != nil {
			return err
		}
		o.ObserveInt64(size, n)
		return nil
	}, size)
	if err != nil {
		return err
	}
	db.regs = append(db.regs, reg)
	return nil
}
