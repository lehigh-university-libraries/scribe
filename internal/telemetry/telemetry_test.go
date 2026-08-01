package telemetry

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/config"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestDisabledTelemetryDoesNotPollOrFailLifecycle(t *testing.T) {
	t.Parallel()

	polled := false
	runtime, err := Start(context.Background(), config.ObservabilityConfig{Exporter: "none"}, Options{
		ServiceName: "scribe-worker",
		QueueSnapshot: func(context.Context) (int64, time.Duration, int64, error) {
			polled = true
			return 0, 0, 0, nil
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if polled {
		t.Fatal("disabled telemetry polled the database")
	}
}

func TestCloudMonitoringResourceAttributeFilterRetainsOnlyBoundedIdentity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value attribute.KeyValue
		want  bool
	}{
		{name: "service name", value: attribute.String("service.name", "scribe-api"), want: true},
		{name: "service namespace", value: attribute.String("service.namespace", "scribe"), want: true},
		{name: "service instance", value: attribute.String("service.instance.id", "opaque-instance"), want: true},
		{name: "deployment environment", value: deploymentEnvironmentAttributeKey.String("prod"), want: true},
		{name: "empty deployment environment", value: deploymentEnvironmentAttributeKey.String(""), want: false},
		{name: "unrelated resource attribute", value: attribute.String("host.name", "sensitive-hostname"), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := cloudMonitoringResourceAttributeFilter(test.value); got != test.want {
				t.Fatalf("cloudMonitoringResourceAttributeFilter(%v) = %t, want %t", test.value, got, test.want)
			}
		})
	}
}

func TestQueueMonitorRecordsContentFreeGauges(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	monitor, err := newQueueMonitor(
		provider.Meter(instrumentationName),
		time.Minute,
		time.Second,
		func(context.Context) (int64, time.Duration, int64, error) {
			return 7, 90 * time.Second, 2, nil
		},
	)
	if err != nil {
		t.Fatalf("newQueueMonitor: %v", err)
	}
	monitor.collect(context.Background())

	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &metrics); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	depth := findMetric(t, metrics, "scribe.transcription.queue.depth")
	depthGauge, ok := depth.Data.(metricdata.Gauge[int64])
	if !ok || len(depthGauge.DataPoints) != 1 || depthGauge.DataPoints[0].Value != 7 {
		t.Fatalf("depth metric = %#v", depth.Data)
	}
	if len(depthGauge.DataPoints[0].Attributes.ToSlice()) != 0 {
		t.Fatalf("depth labels = %v, want none", depthGauge.DataPoints[0].Attributes.ToSlice())
	}
	oldest := findMetric(t, metrics, "scribe.transcription.queue.oldest_age")
	oldestGauge, ok := oldest.Data.(metricdata.Gauge[float64])
	if !ok || len(oldestGauge.DataPoints) != 1 || oldestGauge.DataPoints[0].Value != 90 {
		t.Fatalf("oldest-age metric = %#v", oldest.Data)
	}
	if len(oldestGauge.DataPoints[0].Attributes.ToSlice()) != 0 {
		t.Fatalf("oldest-age labels = %v, want none", oldestGauge.DataPoints[0].Attributes.ToSlice())
	}
	expired := findMetric(t, metrics, "scribe.transcription.queue.expired_leases")
	expiredGauge, ok := expired.Data.(metricdata.Gauge[int64])
	if !ok || len(expiredGauge.DataPoints) != 1 || expiredGauge.DataPoints[0].Value != 2 {
		t.Fatalf("expired-leases metric = %#v", expired.Data)
	}
	if len(expiredGauge.DataPoints[0].Attributes.ToSlice()) != 0 {
		t.Fatalf("expired-leases labels = %v, want none", expiredGauge.DataPoints[0].Attributes.ToSlice())
	}
}

func TestQueueMonitorRedactsQueryFailures(t *testing.T) {
	const sensitive = "PRIVATE_DSN_user:password@database.internal"
	previousLogger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	monitor, err := newQueueMonitor(
		provider.Meter(instrumentationName),
		time.Minute,
		time.Second,
		func(context.Context) (int64, time.Duration, int64, error) {
			return 0, 0, 0, errors.New(sensitive)
		},
	)
	if err != nil {
		t.Fatalf("newQueueMonitor: %v", err)
	}
	monitor.collect(context.Background())
	if strings.Contains(logs.String(), sensitive) || strings.Contains(logs.String(), "password") {
		t.Fatalf("telemetry log exposed query details: %s", logs.String())
	}
	if !strings.Contains(logs.String(), `"msg":"transcription queue telemetry query failed"`) ||
		!strings.Contains(logs.String(), `"error_type":"*errors.errorString"`) {
		t.Fatalf("telemetry log omitted safe diagnostic fields: %s", logs.String())
	}

	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &metrics); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	metricValue := findMetric(t, metrics, "scribe.telemetry.queue.collection_errors")
	sum, ok := metricValue.Data.(metricdata.Sum[int64])
	if !ok || len(sum.DataPoints) != 1 || sum.DataPoints[0].Value != 1 {
		t.Fatalf("collection error metric = %#v", metricValue.Data)
	}
}

func TestQueueMonitorStopsBlockedQueryOnCancellation(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	started := make(chan struct{})
	monitor, err := newQueueMonitor(
		provider.Meter(instrumentationName),
		time.Minute,
		time.Minute,
		func(ctx context.Context) (int64, time.Duration, int64, error) {
			close(started)
			<-ctx.Done()
			return 0, 0, 0, ctx.Err()
		},
	)
	if err != nil {
		t.Fatalf("newQueueMonitor: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		monitor.run(ctx)
	}()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("queue monitor did not stop after cancellation")
	}
}

func findMetric(t *testing.T, resourceMetrics metricdata.ResourceMetrics, name string) metricdata.Metrics {
	t.Helper()
	for _, scope := range resourceMetrics.ScopeMetrics {
		for _, candidate := range scope.Metrics {
			if candidate.Name == name {
				return candidate
			}
		}
	}
	t.Fatalf("metric %q was not collected", name)
	return metricdata.Metrics{}
}
