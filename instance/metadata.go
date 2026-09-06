// Package instance 定义 Registry service instance 的共享元数据。
package instance

import "fmt"

// StickyMetadataKey 标识 service 是否启用玩家粘性路由。
const StickyMetadataKey = "sticky"

// StickyMetadata 返回启用玩家粘性路由的独立 metadata。
func StickyMetadata() map[string]string {
	return map[string]string{StickyMetadataKey: "true"}
}

// IsSticky 解析粘性声明；未声明时使用非粘性路由。
func IsSticky(metadata map[string]string) (bool, error) {
	value, exists := metadata[StickyMetadataKey]
	if !exists {
		return false, nil
	}
	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("instance: invalid sticky metadata %q", value)
	}
}
