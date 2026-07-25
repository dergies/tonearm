package queue2

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"

	"codeberg.org/dergs/tonearm/pkg/tonearm"
)

var queueIDCounter atomic.Uint64

type PlayQueueEntryID uint64

type PlayQueueEntry struct {
	tonearm.Track
	QueueID PlayQueueEntryID
}

type PlayQueue struct {
	mu sync.RWMutex

	// Entries contains all the tracks that have been queued or were queued in the past.
	entries map[PlayQueueEntryID]PlayQueueEntry

	// History contains the IDs of the tracks that have been played in the past.
	history []PlayQueueEntryID
	// UserQueue contains the IDs of the tracks that have been queued by the user.
	userQueue []PlayQueueEntryID
	// BaseQueue contains the IDs of the tracks that have been queued by the source (e.g. albums or playlists).
	baseQueue []PlayQueueEntryID
	// SourceOrder contains the order in which the tracks originally appeared in the source (e.g. album or playlist).
	sourceOrder []PlayQueueEntryID

	// cancelQueueWorker allows the queue to abort an existing queue worker.
	cancelQueueWorker context.CancelFunc
}

// Resets the queue and history to zero and re-fills it from the provided paginator.
// If no paginator is provided, the queue will simply be cleared.
func (q *PlayQueue) Reset(paginator tonearm.Paginator[tonearm.Track]) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.cancelQueueWorker != nil {
		q.cancelQueueWorker()
		q.cancelQueueWorker = nil
	}

	q.entries = make(map[PlayQueueEntryID]PlayQueueEntry)
	q.history = make([]PlayQueueEntryID, 0)
	q.userQueue = make([]PlayQueueEntryID, 0)
	q.baseQueue = make([]PlayQueueEntryID, 0)
	q.sourceOrder = make([]PlayQueueEntryID, 0)

	if paginator == nil {
		return nil
	}

	// We fetch the first page of the paginator and add it to the queue so the UI
	// has something to work with while we wait for the queue to be filled completely.
	page, err := paginator.NextPage()
	if err != nil {
		return err
	}

	trackIds := make([]PlayQueueEntryID, 0, len(page))
	for _, track := range page {
		queueId := PlayQueueEntryID(queueIDCounter.Add(1))
		q.entries[queueId] = PlayQueueEntry{
			Track:   track,
			QueueID: queueId,
		}
		trackIds = append(trackIds, queueId)
	}
	q.baseQueue = trackIds
	q.sourceOrder = trackIds

	if !paginator.IsConsumed() {
		ctx, cancel := context.WithCancel(context.Background())
		q.cancelQueueWorker = cancel
		go q.queueWorker(ctx, paginator)
	}

	return nil
}

func (q *PlayQueue) queueWorker(ctx context.Context, paginator tonearm.Paginator[tonearm.Track]) {
	slog.Debug("started queue worker")
	defer slog.Debug("queue worker exited")
	for {
		select {
		case <-ctx.Done():
			return
		default:
			page, err := paginator.NextPage()
			if err != nil {
				slog.Error("queue worker failed to retrieve page", "error", err)
				return
			}

			q.mu.Lock()
			trackIds := make([]PlayQueueEntryID, 0, len(page))
			for _, track := range page {
				queueId := PlayQueueEntryID(queueIDCounter.Add(1))
				q.entries[queueId] = PlayQueueEntry{
					Track:   track,
					QueueID: queueId,
				}
				trackIds = append(trackIds, queueId)
			}
			q.baseQueue = trackIds
			q.sourceOrder = trackIds
			q.mu.Unlock()
		}
	}
}
