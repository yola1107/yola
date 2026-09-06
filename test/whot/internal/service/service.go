package service

import (
	"context"

	"yola/node"
	"yola/test/whot/api/v1"
	"yola/test/whot/internal/biz"

	"github.com/google/wire"
)

// ProviderSet wires the Whot command service.
var ProviderSet = wire.NewSet(NewService)

var _ v1.GameServer = (*Service)(nil)

type Service struct {
	uc *biz.Usecase
}

func NewService(uc *biz.Usecase) *Service {
	return &Service{uc: uc}
}

// RegisterNode installs command, push, and disconnect adapters for this service.
func (s *Service) RegisterNode(server *node.Server) {
	v1.RegisterGameServer(server, s)
	s.uc.SetClientPusher(server)
	server.OnDisconnect(s.disconnect)
}

func (s *Service) Drain(ctx context.Context) error {
	return s.uc.Drain(ctx)
}

func (s *Service) disconnect(ctx context.Context, sess node.Session) error {
	return s.uc.Disconnect(ctx, sess)
}
