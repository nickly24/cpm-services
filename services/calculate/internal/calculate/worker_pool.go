package calculate

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
)

// RunPool reserves gradingWorkers slots for initial submissions and one
// independent slot for content preparation/regrading. A large regrade cannot
// occupy the slots needed by an exam's newly accepted submissions. All workers
// share the durable inbox; leases fence callbacks across processes as before.
func (w *Worker) RunPool(ctx context.Context, gradingWorkers int) error {
	if gradingWorkers < 1 || gradingWorkers > 32 {
		return errors.New("invalid grading worker count")
	}
	store, ok := w.Store.(KindProcessingRepository)
	if !ok {
		return errors.New("processing store does not support independent lanes")
	}
	w.stats[0].capacity.Store(int64(gradingWorkers))
	w.stats[1].capacity.Store(1)
	w.stats[2].capacity.Store(1)
	var workers sync.WaitGroup
	for range gradingWorkers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			w.run(ctx, func(ctx context.Context, now time.Time, token string) (Message, error) {
				return store.ClaimKind(ctx, now, token, InitialGradeKind)
			})
		}()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		kinds := [...]string{ContentKind, RegradeKind}
		next := 0
		w.run(ctx, func(ctx context.Context, now time.Time, token string) (Message, error) {
			start := next
			next = (next + 1) % len(kinds)
			for offset := range kinds {
				m, err := store.ClaimKind(ctx, now, token, kinds[(start+offset)%len(kinds)])
				if err == nil || !noJob(err) {
					return m, err
				}
			}
			return Message{}, mongo.ErrNoDocuments
		})
	}()
	workers.Wait()
	return nil
}
