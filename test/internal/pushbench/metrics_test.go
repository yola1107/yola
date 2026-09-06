package pushbench

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestMeasurementsSeparateFailuresAndExcludeWarmup(t *testing.T) {
	measurements := newMeasurements(t)
	measurements.record("push", time.Now().Add(-time.Millisecond), nil)
	measurements.collect(t)
	measurements.record("push", time.Now().Add(-time.Millisecond), nil)
	measurements.record("push", time.Now().Add(-time.Millisecond), errors.New("injected failure"))
	var success, failures uint64
	for _, scope := range measurements.collect(t).ScopeMetrics {
		for _, instrument := range scope.Metrics {
			if instrument.Name != "pushbench_seconds" {
				continue
			}
			for _, point := range instrument.Data.(metricdata.Histogram[float64]).DataPoints {
				require.GreaterOrEqual(t, point.Sum, 0.001, "duration is recorded in seconds")
				failed, _ := point.Attributes.Value("error")
				if failed.AsBool() {
					failures += point.Count
				} else {
					success += point.Count
				}
			}
		}
	}
	require.Equal(t, uint64(1), success)
	require.Equal(t, uint64(1), failures)
}

func TestPercentileReportsBucketUpperBound(t *testing.T) {
	for _, test := range []struct {
		name     string
		quantile float64
		counts   []uint64
		want     float64
	}{
		{name: "median", quantile: 0.50, counts: []uint64{5, 3, 2, 0}, want: 1},
		{name: "tail", quantile: 0.99, counts: []uint64{5, 3, 2, 0}, want: 3},
		{name: "overflow", quantile: 0.99, counts: []uint64{5, 3, 1, 1}, want: math.Inf(1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			point := metricdata.HistogramDataPoint[float64]{Count: 10, Bounds: []float64{1, 2, 3}, BucketCounts: test.counts}
			require.Equal(t, test.want, percentileUpperBound(point, test.quantile))
		})
	}
}
