package nats

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"yola/event"

	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

func TestBrokerPayloadLimits(t *testing.T) {
	if address := os.Getenv("YOLA_NATS_URL"); address != "" {
		verifyBrokerPayloadLimits(t, address)
		return
	}
	for _, maximum := range []int32{64 << 10, 1 << 20} {
		t.Run(fmt.Sprintf("maximum=%d", maximum), func(t *testing.T) {
			options := testServerOptions()
			options.MaxPayload = maximum
			verifyBrokerPayloadLimits(t, startTestServerWithOptions(t, options).ClientURL())
		})
	}
}

func verifyBrokerPayloadLimits(t *testing.T, address string) {
	t.Helper()
	const applicationLimit = 64 << 10
	bus := newTestBus(t, address)
	maximum := int(bus.conn.MaxPayload())
	require.GreaterOrEqual(t, maximum, applicationLimit)
	t.Logf("broker max_payload=%d, adapter max_payload=%d", maximum, applicationLimit)
	publisher, err := natsgo.Connect(address, natsgo.NoReconnect())
	require.NoError(t, err)
	t.Cleanup(publisher.Close)
	topic := natsgo.NewInbox()
	sub, err := bus.Subscribe(t.Context(), topic, func(context.Context, event.Event) {})
	require.NoError(t, err)
	observer := sub.(event.SubscriptionStatsProvider)
	require.Error(t, bus.Publish(t.Context(), event.Event{Topic: topic, Payload: make([]byte, applicationLimit+1)}))
	var handled, discarded uint64
	sizes := []int{applicationLimit, applicationLimit + 1, maximum, maximum + 1}
	slices.Sort(sizes)
	for _, size := range slices.Compact(sizes) {
		err = publisher.Publish(topic, make([]byte, size))
		if size > maximum {
			// nats.go 根据 INFO 在本地拒绝；下方原始协议再验证 broker 的独立防线。
			require.ErrorIs(t, err, natsgo.ErrMaxPayload)
			continue
		}
		require.NoError(t, err)
		if size > applicationLimit {
			discarded++
		} else {
			handled++
		}
	}
	require.NoError(t, publisher.FlushTimeout(time.Second))
	require.Eventually(t, func() bool {
		stats := observer.SubscriptionStats()
		return stats.HandlerCalls == handled && stats.PayloadDropped == discarded
	}, time.Second, time.Millisecond)
	require.Zero(t, observer.SubscriptionStats().QueueDropped)

	endpoint, err := url.Parse(address)
	require.NoError(t, err)
	require.Equal(t, "nats", endpoint.Scheme, "raw protocol probe requires the isolated plaintext broker")
	require.Nil(t, endpoint.User, "raw protocol probe does not accept credentials")
	conn, err := net.DialTimeout("tcp", endpoint.Host, time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	require.NoError(t, conn.SetDeadline(time.Now().Add(time.Second)))
	reader := bufio.NewReader(conn)
	info, err := reader.ReadString('\n')
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(info, "INFO "))
	_, err = fmt.Fprintf(conn, "CONNECT {\"verbose\":false}\r\nPUB %s %d\r\n", topic, maximum+1)
	require.NoError(t, err)
	reply, err := reader.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "-ERR 'Maximum Payload Violation'\r\n", reply)
}
