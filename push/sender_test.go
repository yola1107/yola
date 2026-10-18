package push_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	clusterv1 "yola/api/cluster/v1"
	"yola/locate"
	"yola/push"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestSenderPushesToCurrentBindingInTargetNamespace(t *testing.T) {
	gateway := &gatewayAdapter{pushes: make(chan *clusterv1.PushRequest, 1)}
	endpoint := startGateway(t, gateway)
	binding := testBinding("target-game", "player-a", endpoint, "connection-a", "binding-a")
	lookup := lookupFunc(func(_ context.Context, serviceName, uid string) (locate.GateLease, error) {
		require.Equal(t, "target-game", serviceName)
		require.Equal(t, "player-a", uid)
		return locate.GateLease{Binding: binding, TTL: time.Minute}, nil
	})
	sender := newTestSender(t, lookup, push.Timeout(time.Second))

	message := wrapperspb.String("hello")
	require.NoError(t, sender.PushToUID(t.Context(), "target-game", "player-a", 23, message))

	received := receive(t, gateway.pushes)
	wantBody, err := proto.Marshal(message)
	require.NoError(t, err)
	require.Equal(t, int32(23), received.GetCommand())
	require.Equal(t, wantBody, received.GetBody())
	require.Equal(t, binding.BindingToken, received.GetRoute().GetBindingToken())
	require.Equal(t, binding.ConnID, received.GetRoute().GetConnId())
}

func TestSenderAcceptsValueMessages(t *testing.T) {
	gateway := &gatewayAdapter{pushes: make(chan *clusterv1.PushRequest, 1)}
	binding := testBinding("game", "player-a", startGateway(t, gateway), "connection-a", "binding-a")
	lookup := lookupFunc(func(context.Context, string, string) (locate.GateLease, error) {
		return locate.GateLease{Binding: binding, TTL: time.Minute}, nil
	})
	sender := newTestSender(t, lookup)

	err := sender.PushToUID(t.Context(), "game", "player-a", 1, messageValue{wrapperspb.String("hello")})

	require.NoError(t, err)
	require.Equal(t, []byte{0x0a, 0x05, 'h', 'e', 'l', 'l', 'o'}, receive(t, gateway.pushes).GetBody())
}

func TestSenderSelectsNewBindingAfterReconnect(t *testing.T) {
	firstGateway := &gatewayAdapter{pushes: make(chan *clusterv1.PushRequest, 1)}
	secondGateway := &gatewayAdapter{pushes: make(chan *clusterv1.PushRequest, 1)}
	first := testBinding("game", "player-a", startGateway(t, firstGateway), "connection-a", "binding-a")
	second := testBinding("game", "player-a", startGateway(t, secondGateway), "connection-b", "binding-b")
	current := first
	lookup := lookupFunc(func(context.Context, string, string) (locate.GateLease, error) {
		return locate.GateLease{Binding: current, TTL: time.Minute}, nil
	})
	sender := newTestSender(t, lookup, push.Timeout(time.Second))

	require.NoError(t, sender.PushToUID(t.Context(), "game", "player-a", 1, wrapperspb.String("first")))
	require.Equal(t, "binding-a", receive(t, firstGateway.pushes).GetRoute().GetBindingToken())
	current = second
	require.NoError(t, sender.PushToUID(t.Context(), "game", "player-a", 2, wrapperspb.String("second")))
	require.Equal(t, "binding-b", receive(t, secondGateway.pushes).GetRoute().GetBindingToken())
}

func TestSenderRejectsInvalidTargetMessageAndLease(t *testing.T) {
	valid := testBinding("game", "player-a", "grpc://127.0.0.1:1", "connection-a", "binding-a")
	var typedNil *wrapperspb.StringValue
	tests := []struct {
		name        string
		serviceName string
		uid         string
		message     proto.Message
		lease       locate.GateLease
		wantCode    codes.Code
	}{
		{name: "service", serviceName: "invalid/service", uid: "player-a", message: wrapperspb.String("x"), wantCode: codes.InvalidArgument},
		{name: "uid", serviceName: "game", message: wrapperspb.String("x"), wantCode: codes.InvalidArgument},
		{name: "message", serviceName: "game", uid: "player-a", wantCode: codes.InvalidArgument},
		{name: "typed nil message", serviceName: "game", uid: "player-a", message: typedNil, wantCode: codes.InvalidArgument},
		{name: "expired lease", serviceName: "game", uid: "player-a", message: wrapperspb.String("x"), lease: locate.GateLease{Binding: valid}, wantCode: codes.Internal},
		{name: "invalid binding", serviceName: "game", uid: "player-a", message: wrapperspb.String("x"), lease: locate.GateLease{TTL: time.Minute}, wantCode: codes.Internal},
		{name: "invalid endpoint", serviceName: "game", uid: "player-a", message: wrapperspb.String("x"), lease: locate.GateLease{Binding: testBinding("game", "player-a", "not-an-endpoint", valid.ConnID, valid.BindingToken), TTL: time.Minute}, wantCode: codes.Internal},
		{name: "transport mismatch", serviceName: "game", uid: "player-a", message: wrapperspb.String("x"), lease: locate.GateLease{Binding: testBinding("game", "player-a", "grpcs://127.0.0.1:1", valid.ConnID, valid.BindingToken), TTL: time.Minute}, wantCode: codes.Internal},
		{name: "wrong service", serviceName: "game", uid: "player-a", message: wrapperspb.String("x"), lease: locate.GateLease{Binding: testBinding("other", "player-a", valid.GateEndpoint, valid.ConnID, valid.BindingToken), TTL: time.Minute}, wantCode: codes.Internal},
		{name: "wrong uid", serviceName: "game", uid: "player-a", message: wrapperspb.String("x"), lease: locate.GateLease{Binding: testBinding("game", "player-b", valid.GateEndpoint, valid.ConnID, valid.BindingToken), TTL: time.Minute}, wantCode: codes.Internal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lookup := lookupFunc(func(context.Context, string, string) (locate.GateLease, error) {
				return test.lease, nil
			})
			sender := newTestSender(t, lookup)

			err := sender.PushToUID(t.Context(), test.serviceName, test.uid, 1, test.message)
			require.Equal(t, test.wantCode, status.Code(err))
		})
	}
}

func TestSenderMapsLookupAndGatewayErrors(t *testing.T) {
	rpcError := status.Error(codes.ResourceExhausted, "gateway queue is full")
	gateway := &gatewayAdapter{pushes: make(chan *clusterv1.PushRequest, 1), pushError: rpcError}
	binding := testBinding("game", "player-a", startGateway(t, gateway), "connection-a", "binding-a")
	tests := []struct {
		name        string
		lookupError error
		wantCode    codes.Code
	}{
		{name: "missing", lookupError: locate.ErrGateNotFound, wantCode: codes.NotFound},
		{name: "invalid locator state", lookupError: locate.ErrInvalidGateLease, wantCode: codes.Internal},
		{name: "lookup unavailable", lookupError: errors.New("lookup unavailable"), wantCode: codes.Unavailable},
		{name: "gateway status", wantCode: codes.ResourceExhausted},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lookup := lookupFunc(func(context.Context, string, string) (locate.GateLease, error) {
				if test.lookupError != nil {
					return locate.GateLease{}, test.lookupError
				}
				return locate.GateLease{Binding: binding, TTL: time.Minute}, nil
			})
			sender := newTestSender(t, lookup, push.Timeout(time.Second))

			err := sender.PushToUID(t.Context(), "game", "player-a", 1, wrapperspb.String("x"))
			require.Equal(t, test.wantCode, status.Code(err))
		})
	}
}

func TestSenderCallerCancellationStopsGatewayRPC(t *testing.T) {
	gateway := &gatewayAdapter{
		pushes:   make(chan *clusterv1.PushRequest, 1),
		canceled: make(chan error, 1),
	}
	binding := testBinding("game", "player-a", startGateway(t, gateway), "connection-a", "binding-a")
	lookup := lookupFunc(func(context.Context, string, string) (locate.GateLease, error) {
		return locate.GateLease{Binding: binding, TTL: time.Minute}, nil
	})
	sender := newTestSender(t, lookup)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	pushed := make(chan error, 1)
	go func() { pushed <- sender.PushToUID(ctx, "game", "player-a", 1, wrapperspb.String("x")) }()
	receive(t, gateway.pushes)

	cancel()

	require.ErrorIs(t, receive(t, pushed), context.Canceled)
	require.ErrorIs(t, receive(t, gateway.canceled), context.Canceled)
}

func TestSenderPreservesCallerCancellationDuringClose(t *testing.T) {
	entered := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	lookup := lookupFunc(func(ctx context.Context, _, uid string) (locate.GateLease, error) {
		if uid == "probe" {
			return locate.GateLease{}, locate.ErrGateNotFound
		}
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		return locate.GateLease{}, ctx.Err()
	})
	sender := newTestSender(t, lookup)
	t.Cleanup(unblock)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	pushed := make(chan error, 1)
	go func() { pushed <- sender.PushToUID(ctx, "game", "player-a", 1, wrapperspb.String("x")) }()
	receive(t, entered)
	cancel()
	receive(t, canceled)
	closed := make(chan error, 1)
	go func() { closed <- sender.Close() }()
	require.Eventually(t, func() bool {
		return status.Code(sender.PushToUID(t.Context(), "game", "probe", 1, wrapperspb.String("x"))) == codes.Unavailable
	}, time.Second, time.Millisecond)
	unblock()

	require.ErrorIs(t, receive(t, pushed), context.Canceled)
	require.NoError(t, receive(t, closed))
}

func TestSenderDoesNotBeginLookupAfterCallerCancellation(t *testing.T) {
	lookedUp := false
	lookup := lookupFunc(func(context.Context, string, string) (locate.GateLease, error) {
		lookedUp = true
		return locate.GateLease{}, locate.ErrGateNotFound
	})
	sender := newTestSender(t, lookup)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := sender.PushToUID(ctx, "game", "player-a", 1, wrapperspb.String("x"))

	require.ErrorIs(t, err, context.Canceled)
	require.False(t, lookedUp)
}

func TestSenderTimeoutBoundsGateLookup(t *testing.T) {
	lookup := lookupFunc(func(ctx context.Context, _, _ string) (locate.GateLease, error) {
		<-ctx.Done()
		return locate.GateLease{}, ctx.Err()
	})
	sender := newTestSender(t, lookup, push.Timeout(20*time.Millisecond))
	pushed := make(chan error, 1)
	go func() { pushed <- sender.PushToUID(t.Context(), "game", "player-a", 1, wrapperspb.String("x")) }()

	require.ErrorIs(t, receive(t, pushed), context.DeadlineExceeded)
}

func TestSenderExpiredLookupDoesNotBecomeRouteError(t *testing.T) {
	lookup := lookupFunc(func(ctx context.Context, _, _ string) (locate.GateLease, error) {
		<-ctx.Done()
		return locate.GateLease{}, nil
	})
	sender := newTestSender(t, lookup, push.Timeout(20*time.Millisecond))
	pushed := make(chan error, 1)
	go func() { pushed <- sender.PushToUID(t.Context(), "game", "player-a", 1, wrapperspb.String("x")) }()

	require.ErrorIs(t, receive(t, pushed), context.DeadlineExceeded)
}

func TestSenderTimeoutBoundsGatewayRPC(t *testing.T) {
	gateway := &gatewayAdapter{
		pushes:   make(chan *clusterv1.PushRequest, 1),
		canceled: make(chan error, 1),
	}
	binding := testBinding("game", "player-a", startGateway(t, gateway), "connection-a", "binding-a")
	lookup := lookupFunc(func(context.Context, string, string) (locate.GateLease, error) {
		return locate.GateLease{Binding: binding, TTL: time.Minute}, nil
	})
	sender := newTestSender(t, lookup, push.Timeout(50*time.Millisecond))
	pushed := make(chan error, 1)
	go func() { pushed <- sender.PushToUID(t.Context(), "game", "player-a", 1, wrapperspb.String("x")) }()
	receive(t, gateway.pushes)

	require.ErrorIs(t, receive(t, pushed), context.DeadlineExceeded)
	require.Error(t, receive(t, gateway.canceled))
}

func TestSenderCloseCancelsAndWaitsForLookup(t *testing.T) {
	entered := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	lookup := lookupFunc(func(ctx context.Context, _, _ string) (locate.GateLease, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		return locate.GateLease{}, ctx.Err()
	})
	sender := newTestSender(t, lookup)
	t.Cleanup(unblock)
	pushed := make(chan error, 1)
	go func() { pushed <- sender.PushToUID(t.Context(), "game", "player-a", 1, wrapperspb.String("x")) }()
	receive(t, entered)
	closed := make(chan error, 2)
	for range 2 {
		go func() { closed <- sender.Close() }()
	}
	receive(t, canceled)
	require.Equal(t, codes.Unavailable, status.Code(sender.PushToUID(t.Context(), "game", "player-a", 1, wrapperspb.String("x"))))
	select {
	case err := <-closed:
		t.Fatalf("Close returned before the accepted lookup finished: %v", err)
	default:
	}
	unblock()

	require.Equal(t, codes.Unavailable, status.Code(receive(t, pushed)))
	for range 2 {
		require.NoError(t, receive(t, closed))
	}
	require.NoError(t, sender.Close())
}

func TestSenderCloseCancelsGatewayRPC(t *testing.T) {
	gateway := &gatewayAdapter{
		pushes:   make(chan *clusterv1.PushRequest, 1),
		canceled: make(chan error, 1),
	}
	binding := testBinding("game", "player-a", startGateway(t, gateway), "connection-a", "binding-a")
	lookup := lookupFunc(func(context.Context, string, string) (locate.GateLease, error) {
		return locate.GateLease{Binding: binding, TTL: time.Minute}, nil
	})
	sender := newTestSender(t, lookup)
	pushed := make(chan error, 1)
	go func() { pushed <- sender.PushToUID(t.Context(), "game", "player-a", 1, wrapperspb.String("x")) }()
	receive(t, gateway.pushes)

	require.NoError(t, sender.Close())

	require.Equal(t, codes.Unavailable, status.Code(receive(t, pushed)))
	require.ErrorIs(t, receive(t, gateway.canceled), context.Canceled)
}

type messageValue struct{ *wrapperspb.StringValue }
