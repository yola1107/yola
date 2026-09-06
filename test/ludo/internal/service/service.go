package service

import (
	"context"

	"yola/node"
	"yola/test/ludo/api/v1"
	"yola/test/ludo/internal/biz"

	"github.com/google/wire"
)

var ProviderSet = wire.NewSet(NewService)

var _ v1.GameServer = (*Service)(nil)

type Service struct {
	usecase *biz.Usecase
}

func NewService(usecase *biz.Usecase) *Service {
	return &Service{usecase: usecase}
}

// RegisterNode installs command, push, and disconnect adapters for this service.
func (s *Service) RegisterNode(server *node.Server) {
	v1.RegisterGameServer(server, s)
	s.usecase.SetClientPusher(server)
	server.OnDisconnect(s.disconnect)
}

func (s *Service) Drain(ctx context.Context) error {
	return s.usecase.Drain(ctx)
}

func (s *Service) disconnect(ctx context.Context, sess node.Session) error {
	return s.usecase.Disconnect(ctx, sess)
}
