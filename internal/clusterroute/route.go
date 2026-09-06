// Package clusterroute converts between locator bindings and cluster wire routes.
package clusterroute

import (
	clusterv1 "yola/api/cluster/v1"
	"yola/locate"
)

func FromBinding(binding locate.GateBinding) *clusterv1.GateRoute {
	return &clusterv1.GateRoute{
		ServiceName:  binding.ServiceName,
		Uid:          binding.UID,
		BindingToken: binding.BindingToken,
		GateId:       binding.GateID,
		GateEndpoint: binding.GateEndpoint,
		ConnId:       binding.ConnID,
	}
}

func ToBinding(route *clusterv1.GateRoute) locate.GateBinding {
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
