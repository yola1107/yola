// Package contextwait 在 context 预算内等待异步清理。
package contextwait

import "context"

// Done 等待 done；它与 ctx 同时完成时优先返回清理结果。
// 返回 context 错误意味着清理可能仍在进行。
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
