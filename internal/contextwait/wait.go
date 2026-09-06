// Package contextwait waits for asynchronous cleanup with a context deadline.
package contextwait

import "context"

// Done waits for done and prefers a completed cleanup when ctx becomes done at
// the same time. A context error therefore means cleanup may still be running.
func Done(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		select {
		case <-done:
			return nil
		default:
			return ctx.Err()
		}
	}
}
