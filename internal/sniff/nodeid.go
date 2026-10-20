package sniff

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
)

// ObtainNodeID asks the server for the DHT node ID, retrying after backoff
// until it answers with one or ctx is done. Every failed attempt after the
// first is logged as a warning.
func ObtainNodeID(ctx context.Context, ingest sniffarrv1connect.IngestServiceClient, backoff time.Duration, log *slog.Logger) ([20]byte, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	for attempt := 1; ; attempt++ {
		res, err := ingest.HashKey(ctx, connect.NewRequest(&sniffarrv1.HashKeyRequest{}))
		if err == nil {
			id := res.Msg.GetNodeId()
			if len(id) == 20 && [20]byte(id) != [20]byte{} {
				return [20]byte(id), nil
			}
			err = fmt.Errorf("node ID of %d bytes, want 20 and not zero", len(id))
		}
		if attempt > 1 {
			log.Warn("node id", "err", err, "attempt", attempt)
		} else {
			log.Debug("node id", "err", err, "attempt", attempt)
		}
		if err := sleep(ctx, backoff); err != nil {
			return [20]byte{}, err
		}
	}
}
