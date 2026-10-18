// Package push 提供不依赖 Node 的客户端推送能力。
package push

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"

	"yola/internal/gateclient"
	"yola/locate"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

var errSenderClosed = errors.New("sender is closed")

// GateLocator 查找玩家当前的 Gateway 连接。
// 实现必须响应 ctx 取消；资源生命周期由调用方管理。
type GateLocator interface {
	LocateGate(ctx context.Context, serviceName, uid string) (locate.GateLease, error)
}

// Sender 向玩家当前的 Gateway 连接推送消息。
// Close 会取消并等待已接纳的调用，且可重复调用。
type Sender struct {
	locator GateLocator
	timeout time.Duration
	client  *gateclient.Client

	ctx       context.Context
	cancel    context.CancelCauseFunc
	mu        sync.Mutex
	closed    bool
	calls     sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
}

// NewSender 创建并持有一个 Gateway 回程客户端。
func NewSender(locator GateLocator, opts ...Option) (*Sender, error) {
	if isNil(locator) {
		return nil, errors.New("push: gate locator is required")
	}
	configured, err := resolveOptions(opts...)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	return &Sender{
		locator: locator,
		timeout: configured.timeout,
		client:  gateclient.New(configured.tlsConfig, configured.middlewares...),
		ctx:     ctx,
		cancel:  cancel,
	}, nil
}

// PushToUID 查找目标服务中玩家当前的 Gate binding 并推送消息。
// 目标无效或消息为空返回 InvalidArgument，绑定不存在返回 NotFound，路由损坏返回 Internal。
// 调用方取消和超时保留 context 错误；Gateway gRPC 状态原样返回。
func (s *Sender) PushToUID(
	ctx context.Context,
	targetServiceName string,
	uid string,
	command int32,
	msg proto.Message,
) error {

	if !locate.ValidServiceName(targetServiceName) || !locate.ValidUID(uid) {
		return status.Error(codes.InvalidArgument, "invalid push target")
	}
	if msg == nil || !msg.ProtoReflect().IsValid() {
		return status.Error(codes.InvalidArgument, "push message is required")
	}
	if !s.admit() {
		return status.Error(codes.Unavailable, "sender is closed")
	}
	defer s.calls.Done()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	ctx, cancelClose := context.WithCancelCause(ctx)
	defer cancelClose(nil)
	stop := context.AfterFunc(s.ctx, func() { cancelClose(context.Cause(s.ctx)) })
	defer stop()
	if err := ctx.Err(); err != nil {
		return mapSenderError(ctx, err)
	}

	lease, err := s.locator.LocateGate(ctx, targetServiceName, uid)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return mapSenderError(ctx, err)
	}
	if lease.TTL <= 0 || !locate.ValidGateBinding(lease.Binding) ||
		lease.Binding.ServiceName != targetServiceName || lease.Binding.UID != uid {
		return status.Error(codes.Internal, "invalid gateway route")
	}
	return mapSenderError(ctx, s.client.Push(ctx, lease.Binding, command, msg))
}

// Close 取消已接纳调用、关闭内部 Gateway 客户端，并等待调用退出。
// GateLocator 和 middleware 须响应 context 取消；并发或重复 Close 等待同一次清理。
func (s *Sender) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.cancel(errSenderClosed)
		s.mu.Unlock()
		s.closeErr = s.client.Close()
		s.calls.Wait()
	})
	return s.closeErr
}

func (s *Sender) admit() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.calls.Add(1)
	return true
}

func mapSenderError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(context.Cause(ctx), errSenderClosed) {
		return status.Error(codes.Unavailable, "sender is closed")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, locate.ErrGateNotFound) {
		return status.Error(codes.NotFound, "gate binding not found")
	}
	if errors.Is(err, locate.ErrInvalidGateBinding) || errors.Is(err, locate.ErrInvalidGateLease) ||
		errors.Is(err, gateclient.ErrInvalidEndpoint) || errors.Is(err, gateclient.ErrTransportSecurity) {
		return status.Error(codes.Internal, "invalid gateway route")
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	return status.Error(codes.Unavailable, "gateway is unavailable")
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
