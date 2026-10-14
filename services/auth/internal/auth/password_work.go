package auth

import "context"

// A scrypt verification allocates up to 64 MiB. Queue password work so a burst
// cannot allocate one such buffer per HTTP request. This is not a login rate
// limit: every connected caller waits for a slot instead of receiving 429.
func (s *Server) passwordWork(ctx context.Context, check func() bool) (bool, error) {
	s.passwordOnce.Do(func() {
		s.passwordSlots = make(chan struct{}, 12)
	})
	if err := ctx.Err(); err != nil {
		return false, err
	}
	select {
	case s.passwordSlots <- struct{}{}:
		defer func() { <-s.passwordSlots }()
	case <-ctx.Done():
		return false, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return check(), nil
}
