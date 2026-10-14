package auth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPasswordWorkQueuesBurstAndHonorsCancellation(t *testing.T) {
	s := &Server{}
	_, _ = s.passwordWork(context.Background(), func() bool { return true })
	capacity := cap(s.passwordSlots)
	var active atomic.Int32
	entered := make(chan struct{}, capacity)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for range capacity {
		wg.Go(func() {
			_, _ = s.passwordWork(context.Background(), func() bool {
				active.Add(1)
				entered <- struct{}{}
				<-release
				active.Add(-1)
				return true
			})
		})
	}
	defer wg.Wait()
	defer close(release)
	for range capacity {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("password slots were not filled")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := s.passwordWork(ctx, func() bool {
			t.Error("cancelled queued work must not execute")
			return true
		})
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued cancellation did not finish")
	}
	if active.Load() != int32(capacity) {
		t.Fatal("cancelled waiter changed active password work")
	}
}
