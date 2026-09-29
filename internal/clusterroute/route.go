// Package clusterroute 转换定位 binding 与集群协议路由。
package clusterroute

import (
	"yola/api/cluster/v1"
	"yola/locate"
)

func FromBinding(binding locate.GateBinding) *v1.GateRoute {
	return &v1.GateRoute{
		ServiceName:  binding.ServiceName,
		Uid:          binding.UID,
		BindingToken: binding.BindingToken,
		GateId:       binding.GateID,
		GateEndpoint: binding.GateEndpoint,
		ConnId:       binding.ConnID,
	}
}

func ToBinding(route *v1.GateRoute) locate.GateBinding {
	if route == nil {
		return locate.GateBinding{}
	}
	return locate.GateBinding{
		ServiceName:  route.GetServiceName(),
		UID:          route.GetUid(),
		BindingToken: route.GetBindingToken(),
		GateID:       route.GetGateId(),
		GateEndpoint: route.GetGateEndpoint(),
		ConnID:       route.GetConnId(),
	}
}
