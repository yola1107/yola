package service

import (
	"context"

	"yola/node"
	v1 "yola/test/ludo/api/v1"
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

// RegisterNode 注册命令，并接入推送和断线处理。
func (s *Service) RegisterNode(server *node.Server) {
	v1.RegisterGameServer(server, s)
	s.usecase.SetClientPusher(server)
	server.OnDisconnect(func(ctx context.Context, sess node.Session) error {
		return s.usecase.Disconnect(ctx, sess)
	})
}
