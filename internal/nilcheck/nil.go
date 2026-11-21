// Package nilcheck 检查依赖接口及其持有的类型化 nil。
package nilcheck

import "reflect"

// IsNil 报告 value 是否为 nil 或持有类型化 nil。
func IsNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
