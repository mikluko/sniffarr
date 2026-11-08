package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"connectrpc.com/connect"

	sniffarrv1 "sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
)

// journalPage and journalPages bound how much of the journal seeds reads.
const (
	journalPage  = 1000
	journalPages = 20
)

// seeds returns up to n infohashes, newest first, of probes that
// downloaded pieces, and so left the torrent seeded, no later than before.
func seeds(ctx context.Context, c sniffarrv1connect.BrowseServiceClient, before time.Time, n int) ([][20]byte, error) {
	var (
		out  [][20]byte
		seen = map[[20]byte]bool{}
		page int64
	)
	for range journalPages {
		resp, err := c.Journal(ctx, connect.NewRequest(&sniffarrv1.JournalRequest{Kind: "probed", Limit: journalPage, Before: page}))
		if err != nil {
			return nil, fmt.Errorf("journal: %w", err)
		}
		es := resp.Msg.GetEntries()
		if len(es) == 0 {
			break
		}
		for i := len(es) - 1; i >= 0 && len(out) < n; i-- {
			h, ok, err := seeded(es[i], before)
			if err != nil {
				return nil, err
			}
			if ok && !seen[h] {
				seen[h] = true
				out = append(out, h)
			}
		}
		if len(out) == n {
			break
		}
		page = es[0].GetId()
	}
	return out, nil
}

// seeded returns the infohash of a probed entry no later than before whose
// probe downloaded pieces.
func seeded(e *sniffarrv1.JournalEntry, before time.Time) ([20]byte, bool, error) {
	var h [20]byte
	if e.GetAt().AsTime().After(before) || len(e.GetInfohash()) != len(h) {
		return h, false, nil
	}
	var d struct {
		Downloaded int64 `json:"downloaded"`
	}
	if e.GetDetail() != "" {
		if err := json.Unmarshal([]byte(e.GetDetail()), &d); err != nil {
			return h, false, fmt.Errorf("journal entry %d: %w", e.GetId(), err)
		}
	}
	copy(h[:], e.GetInfohash())
	return h, d.Downloaded > 0, nil
}
