package pushbench

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/go-kratos/kratos/contrib/otel/v3/metrics"
	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

type measurements struct {
	reader  *sdkmetric.ManualReader
	seconds metric.Float64Histogram
	rpc     middleware.Middleware
}

func newMeasurements(t testing.TB) *measurements {
	t.Helper()
	reader := sdkmetric.NewManualReader(sdkmetric.WithTemporalitySelector(func(sdkmetric.InstrumentKind) metricdata.Temporality {
		return metricdata.DeltaTemporality
	}))
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	meter := provider.Meter("yola.test.pushbench")
	// 对数桶用于分段诊断；报告分位数所在桶的上界，不把它当成精确分位数。
	var bounds []float64
	for bound := 0.000001; bound < 30; bound *= 1.2 {
		bounds = append(bounds, bound)
	}
	seconds, err := meter.Float64Histogram("pushbench_seconds", metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(bounds...))
	require.NoError(t, err)
	rpcSeconds, err := meter.Float64Histogram("pushbench_rpc_seconds", metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(bounds...))
	require.NoError(t, err)
	return &measurements{
		reader:  reader,
		seconds: seconds,
		rpc:     metrics.Client(metrics.WithSeconds(rpcSeconds)),
	}
}

func (m *measurements) record(stage string, started time.Time, err error) {
	attributes := metric.WithAttributes(attribute.String("stage", stage), attribute.Bool("error", err != nil))
	m.seconds.Record(context.Background(), time.Since(started).Seconds(), attributes)
}

func (m *measurements) collect(t testing.TB) metricdata.ResourceMetrics {
	t.Helper()
	var result metricdata.ResourceMetrics
	require.NoError(t, m.reader.Collect(context.Background(), &result))
	return result
}

type measurementSummary struct {
	Stage    string  `json:"stage"`
	Calls    uint64  `json:"calls"`
	MeanUS   float64 `json:"mean_us"`
	P50Upper float64 `json:"p50_le_us"`
	P95Upper float64 `json:"p95_le_us"`
	P99Upper float64 `json:"p99_le_us"`
	Failed   bool    `json:"failed"`
}

func (m *measurements) report(b *testing.B) {
	b.Helper()
	var summaries []measurementSummary
	for _, scope := range m.collect(b).ScopeMetrics {
		for _, instrument := range scope.Metrics {
			histogram, ok := instrument.Data.(metricdata.Histogram[float64])
			if !ok {
				continue
			}
			for _, point := range histogram.DataPoints {
				if point.Count == 0 {
					continue
				}
				stage, _ := point.Attributes.Value("stage")
				failed, _ := point.Attributes.Value("error")
				name := stage.AsString()
				if instrument.Name == "pushbench_rpc_seconds" {
					name = "gateway_rpc"
				}
				summary := measurementSummary{
					Stage: name, Calls: point.Count, MeanUS: point.Sum * 1e6 / float64(point.Count),
					P50Upper: percentileUpperBound(point, 0.50) * 1e6,
					P95Upper: percentileUpperBound(point, 0.95) * 1e6,
					P99Upper: percentileUpperBound(point, 0.99) * 1e6,
					Failed:   failed.AsBool(),
				}
				summaries = append(summaries, summary)
				b.ReportMetric(summary.MeanUS, name+"_mean_us")
				b.ReportMetric(summary.P99Upper, name+"_p99_le_us")
				if summary.Failed {
					b.Errorf("%s had %d failed calls", name, point.Count)
				}
			}
		}
	}
	encoded, err := json.Marshal(summaries)
	require.NoError(b, err)
	b.Logf("I45 measurements: %s", encoded)
}

func percentileUpperBound(point metricdata.HistogramDataPoint[float64], quantile float64) float64 {
	threshold := uint64(math.Ceil(float64(point.Count) * quantile))
	var count uint64
	for index, bucket := range point.BucketCounts {
		count += bucket
		if count < threshold {
			continue
		}
		if index < len(point.Bounds) {
			return point.Bounds[index]
		}
		break
	}
	return math.Inf(1)
}
