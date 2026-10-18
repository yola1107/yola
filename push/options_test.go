package push_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	clusterv1 "yola/api/cluster/v1"
	"yola/locate"
	"yola/push"

	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/transport"
	kgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestSenderRejectsInvalidConfiguration(t *testing.T) {
	lookup := lookupFunc(func(context.Context, string, string) (locate.GateLease, error) {
		return locate.GateLease{}, locate.ErrGateNotFound
	})
	tests := []struct {
		name    string
		locator push.GateLocator
		options []push.Option
	}{
		{name: "missing locator"},
		{name: "typed nil locator", locator: lookupFunc(nil)},
		{name: "missing option", locator: lookup, options: []push.Option{nil}},
		{name: "zero timeout", locator: lookup, options: []push.Option{push.Timeout(0)}},
		{name: "negative timeout", locator: lookup, options: []push.Option{push.Timeout(-time.Second)}},
		{name: "missing TLS", locator: lookup, options: []push.Option{push.ClientTLS(nil)}},
		{name: "unverified TLS", locator: lookup, options: []push.Option{push.ClientTLS(&tls.Config{InsecureSkipVerify: true})}},
		{name: "missing middleware", locator: lookup, options: []push.Option{push.ClientMiddleware(nil)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sender, err := push.NewSender(test.locator, test.options...)
			if sender != nil {
				t.Cleanup(func() { require.NoError(t, sender.Close()) })
			}
			require.Error(t, err)
			require.Nil(t, sender)
		})
	}
}

func TestSenderSharesTimeoutWithLookupAndClientMiddleware(t *testing.T) {
	for _, test := range []struct {
		name    string
		timeout time.Duration
		caller  bool
	}{
		{name: "default", timeout: 3 * time.Second},
		{name: "configured", timeout: time.Second},
		{name: "caller deadline", timeout: 3 * time.Second, caller: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			gateway := &gatewayAdapter{pushes: make(chan *clusterv1.PushRequest, 1)}
			binding := testBinding("game", "player-a", startGateway(t, gateway), "connection-a", "binding-a")
			var lookupDeadline, rpcDeadline time.Time
			lookup := lookupFunc(func(ctx context.Context, _, _ string) (locate.GateLease, error) {
				lookupDeadline, _ = ctx.Deadline()
				return locate.GateLease{Binding: binding, TTL: time.Minute}, nil
			})
			observe := func(next middleware.Handler) middleware.Handler {
				return func(ctx context.Context, request any) (any, error) {
					info, ok := transport.FromClientContext(ctx)
					require.True(t, ok)
					require.Equal(t, clusterv1.Gateway_Push_FullMethodName, info.Operation())
					rpcDeadline, _ = ctx.Deadline()
					return next(ctx, request)
				}
			}
			opts := []push.Option{push.ClientMiddleware(observe)}
			if test.name == "configured" {
				opts = append(opts, push.Timeout(test.timeout))
			}
			sender := newTestSender(t, lookup, opts...)
			ctx := t.Context()
			if test.caller {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Second)
				defer cancel()
			}
			before := time.Now()

			require.NoError(t, sender.PushToUID(ctx, "game", "player-a", 1, wrapperspb.String("x")))

			require.Equal(t, lookupDeadline, rpcDeadline)
			if test.caller {
				deadline, _ := ctx.Deadline()
				require.Equal(t, deadline, lookupDeadline)
			} else {
				require.False(t, lookupDeadline.Before(before.Add(test.timeout)))
				require.False(t, lookupDeadline.After(time.Now().Add(test.timeout)))
			}
			receive(t, gateway.pushes)
		})
	}
}

func TestSenderPreservesClientMiddlewareStatus(t *testing.T) {
	want := status.Error(codes.ResourceExhausted, "middleware rejected the push")
	lookup := lookupFunc(func(context.Context, string, string) (locate.GateLease, error) {
		return locate.GateLease{
			Binding: testBinding("game", "player-a", "grpc://127.0.0.1:1", "connection-a", "binding-a"),
			TTL:     time.Minute,
		}, nil
	})
	sender := newTestSender(t, lookup, push.ClientMiddleware(func(middleware.Handler) middleware.Handler {
		return func(context.Context, any) (any, error) { return nil, want }
	}))

	require.ErrorIs(t, sender.PushToUID(t.Context(), "game", "player-a", 1, wrapperspb.String("x")), want)
}

func TestSenderUsesVerifiedClientTLS(t *testing.T) {
	certificates := httptest.NewTLSServer(http.NotFoundHandler())
	serverTLS := certificates.TLS.Clone()
	roots := x509.NewCertPool()
	roots.AddCert(certificates.Certificate())
	certificates.Close()
	gateway := &gatewayAdapter{pushes: make(chan *clusterv1.PushRequest, 1)}
	endpoint := startGateway(t, gateway, kgrpc.TLSConfig(serverTLS))
	binding := testBinding("game", "player-a", "grpcs"+endpoint[len("grpc"):], "connection-a", "binding-a")
	lookup := lookupFunc(func(context.Context, string, string) (locate.GateLease, error) {
		return locate.GateLease{Binding: binding, TTL: time.Minute}, nil
	})
	config := &tls.Config{RootCAs: roots}
	sender := newTestSender(t, lookup, push.ClientTLS(config))
	config.ServerName = "changed.invalid"

	require.NoError(t, sender.PushToUID(t.Context(), "game", "player-a", 1, wrapperspb.String("x")))
	require.Equal(t, binding.BindingToken, receive(t, gateway.pushes).GetRoute().GetBindingToken())
}
