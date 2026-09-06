package server

import "github.com/google/wire"

// ProviderSet wires the Yola Node transport.
var ProviderSet = wire.NewSet(NewNodeServer)
