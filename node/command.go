package node

import "context"

type commandKey struct{}

// CommandFromContext 返回当前 Forward 的业务 command；它不改变 Kratos transport operation。
// context 未携带该元数据时返回 false，command 为 0 时仍可返回 true。
func CommandFromContext(ctx context.Context) (int32, bool) {
	if ctx == nil {
		return 0, false
	}
	command, ok := ctx.Value(commandKey{}).(int32)
	return command, ok
}
