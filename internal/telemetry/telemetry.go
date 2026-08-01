// Package telemetry owns Scribe's bounded OpenTelemetry SDK lifecycle.
package telemetry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"sync"
	"time"

	googlemetric "github.com/GoogleCloudPlatform/opentelemetry-operations-go/exporter/metric"
	googletrace "github.com/GoogleCloudPlatform/opentelemetry-operations-go/exporter/trace"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/safelog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const instrumentationName = "github.com/lehigh-university-libraries/scribe"

const deploymentEnvironmentAttributeKey = attribute.Key("deployment.environment.name")

var serviceNamePattern = regexp.MustCompile(`^scribe-[a-z0-9-]{1,48}$`)

// QueueSnapshotFunc reads the deployment-wide claimable transcription queue.
// The implementation must not add workspace- or job-scoped dimensions.
type QueueSnapshotFunc func(context.Context) (depth int64, oldestAge time.Duration, expiredLeases int64, err error)

// Options identifies one process and optionally enables worker-owned queue
// sampling. ServiceName is code-owned rather than workspace input.
type Options struct {
	ServiceName   string
	QueueSnapshot QueueSnapshotFunc
}

// Runtime owns providers and background samplers installed for one process.
// A non-nil Runtime is returned even when an exporter cannot be initialized so
// callers can keep telemetry outside their availability and readiness paths.
type Runtime struct {
	exportTimeout  time.Duration
	meterProvider  *sdkmetric.MeterProvider
	tracerProvider *sdktrace.TracerProvider
	monitorCancel  context.CancelFunc
	monitorDone    <-chan struct{}
	closeOnce      sync.Once
	closeErr       error
}

// Start initializes the configured exporter and starts the optional queue
// sampler. Exporter errors are returned for safe categorical logging, but any
// successfully initialized provider remains usable.
func Start(ctx context.Context, cfg config.ObservabilityConfig, opts Options) (*Runtime, error) {
	runtime := &Runtime{exportTimeout: cfg.ExportTimeout}
	if cfg.Exporter == "none" || cfg.Exporter == "" {
		return runtime, nil
	}
	if !serviceNamePattern.MatchString(opts.ServiceName) {
		return runtime, fmt.Errorf("telemetry service name is invalid")
	}

	initCtx, cancel := context.WithTimeout(ctx, cfg.ExportTimeout)
	defer cancel()

	res := resource.NewSchemaless(
		attribute.String("service.name", opts.ServiceName),
		attribute.String("service.namespace", "scribe"),
		attribute.String("service.instance.id", opaqueInstanceID(opts.ServiceName)),
		deploymentEnvironmentAttributeKey.String(cfg.DeploymentEnvironment),
	)
	errorHandler := safeErrorHandler{}
	otel.SetErrorHandler(errorHandler)

	var startErrors []error
	metricOptions := []googlemetric.Option{
		googlemetric.WithContext(initCtx),
		googlemetric.WithFilteredResourceAttributes(cloudMonitoringResourceAttributeFilter),
	}
	traceOptions := []googletrace.Option{
		googletrace.WithContext(initCtx),
		googletrace.WithTimeout(cfg.ExportTimeout),
		googletrace.WithErrorHandler(errorHandler),
	}
	if cfg.GoogleProjectID != "" {
		metricOptions = append(metricOptions, googlemetric.WithProjectID(cfg.GoogleProjectID))
		traceOptions = append(traceOptions, googletrace.WithProjectID(cfg.GoogleProjectID))
	}

	metricExporter, err := googlemetric.New(metricOptions...)
	if err != nil {
		startErrors = append(startErrors, fmt.Errorf("initialize Google Cloud Monitoring exporter: %w", err))
	} else {
		reader := sdkmetric.NewPeriodicReader(
			metricExporter,
			sdkmetric.WithInterval(cfg.MetricExportInterval),
			sdkmetric.WithTimeout(cfg.ExportTimeout),
		)
		runtime.meterProvider = sdkmetric.NewMeterProvider(
			sdkmetric.WithResource(res),
			sdkmetric.WithReader(reader),
		)
		otel.SetMeterProvider(runtime.meterProvider)
	}

	traceExporter, err := googletrace.New(traceOptions...)
	if err != nil {
		startErrors = append(startErrors, fmt.Errorf("initialize Google Cloud Trace exporter: %w", err))
	} else {
		runtime.tracerProvider = sdktrace.NewTracerProvider(
			sdktrace.WithResource(res),
			sdktrace.WithSampler(sdktrace.TraceIDRatioBased(cfg.TraceSampleRatio)),
			sdktrace.WithBatcher(
				traceExporter,
				sdktrace.WithExportTimeout(cfg.ExportTimeout),
				sdktrace.WithBatchTimeout(5*time.Second),
				sdktrace.WithMaxQueueSize(2048),
				sdktrace.WithMaxExportBatchSize(256),
			),
		)
		otel.SetTracerProvider(runtime.tracerProvider)
		otel.SetTextMapPropagator(propagation.TraceContext{})
	}

	if runtime.meterProvider != nil && opts.QueueSnapshot != nil {
		monitor, monitorErr := newQueueMonitor(
			runtime.meterProvider.Meter(instrumentationName),
			cfg.QueuePollInterval,
			cfg.ExportTimeout,
			opts.QueueSnapshot,
		)
		if monitorErr != nil {
			startErrors = append(startErrors, fmt.Errorf("initialize transcription queue telemetry: %w", monitorErr))
		} else {
			monitorCtx, monitorCancel := context.WithCancel(ctx)
			done := make(chan struct{})
			runtime.monitorCancel = monitorCancel
			runtime.monitorDone = done
			go func() {
				defer close(done)
				monitor.run(monitorCtx)
			}()
		}
	}

	return runtime, errors.Join(startErrors...)
}

// cloudMonitoringResourceAttributeFilter preserves the Google exporter's
// bounded service identity labels and adds the code-owned deployment
// environment. All other resource attributes remain excluded from metric
// labels so process or cloud metadata cannot create unbounded series.
func cloudMonitoringResourceAttributeFilter(value attribute.KeyValue) bool {
	if value.Key == deploymentEnvironmentAttributeKey {
		return value.Value.Type() == attribute.STRING && value.Value.AsString() != ""
	}
	return googlemetric.DefaultResourceAttributesFilter(value)
}

// Close stops SQL sampling before flushing providers. All waits share one
// fixed deadline so an unavailable telemetry backend cannot delay shutdown
// indefinitely.
func (runtime *Runtime) Close() error {
	if runtime == nil {
		return nil
	}
	runtime.closeOnce.Do(func() {
		timeout := runtime.exportTimeout
		if timeout <= 0 {
			timeout = config.DefaultTelemetryExportTimeout
		}
		closeCtx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		if runtime.monitorCancel != nil {
			runtime.monitorCancel()
		}
		if runtime.monitorDone != nil {
			select {
			case <-runtime.monitorDone:
			case <-closeCtx.Done():
				runtime.closeErr = errors.Join(runtime.closeErr, closeCtx.Err())
			}
		}
		if runtime.meterProvider != nil {
			runtime.closeErr = errors.Join(runtime.closeErr, runtime.meterProvider.Shutdown(closeCtx))
		}
		if runtime.tracerProvider != nil {
			runtime.closeErr = errors.Join(runtime.closeErr, runtime.tracerProvider.Shutdown(closeCtx))
		}
	})
	return runtime.closeErr
}

func opaqueInstanceID(serviceName string) string {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = serviceName
	}
	digest := sha256.Sum256([]byte(serviceName + "\x00" + hostname))
	return hex.EncodeToString(digest[:8])
}

type safeErrorHandler struct{}

func (safeErrorHandler) Handle(err error) {
	if err == nil {
		return
	}
	slog.Warn(
		"telemetry export failed",
		"error_type", safelog.ErrorType(err),
		"category", safelog.ErrorCategory(err),
	)
}

type queueMonitor struct {
	interval         time.Duration
	queryTimeout     time.Duration
	snapshot         QueueSnapshotFunc
	depth            metric.Int64Gauge
	oldestAgeSeconds metric.Float64Gauge
	expiredLeases    metric.Int64Gauge
	collectionErrors metric.Int64Counter
}

func newQueueMonitor(meter metric.Meter, interval, queryTimeout time.Duration, snapshot QueueSnapshotFunc) (*queueMonitor, error) {
	depth, err := meter.Int64Gauge(
		"scribe.transcription.queue.depth",
		metric.WithDescription("Number of transcription jobs claimable by a worker now."),
		metric.WithUnit("{job}"),
	)
	if err != nil {
		return nil, err
	}
	oldestAge, err := meter.Float64Gauge(
		"scribe.transcription.queue.oldest_age",
		metric.WithDescription("Age in seconds of the oldest transcription job claimable now."),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}
	expiredLeases, err := meter.Int64Gauge(
		"scribe.transcription.queue.expired_leases",
		metric.WithDescription("Number of running transcription jobs with an expired worker lease."),
		metric.WithUnit("{job}"),
	)
	if err != nil {
		return nil, err
	}
	collectionErrors, err := meter.Int64Counter(
		"scribe.telemetry.queue.collection_errors",
		metric.WithDescription("Number of failed transcription queue telemetry queries."),
		metric.WithUnit("{error}"),
	)
	if err != nil {
		return nil, err
	}
	return &queueMonitor{
		interval:         interval,
		queryTimeout:     queryTimeout,
		snapshot:         snapshot,
		depth:            depth,
		oldestAgeSeconds: oldestAge,
		expiredLeases:    expiredLeases,
		collectionErrors: collectionErrors,
	}, nil
}

func (monitor *queueMonitor) run(ctx context.Context) {
	monitor.collect(ctx)
	ticker := time.NewTicker(monitor.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			monitor.collect(ctx)
		}
	}
}

func (monitor *queueMonitor) collect(ctx context.Context) {
	queryCtx, cancel := context.WithTimeout(ctx, monitor.queryTimeout)
	defer cancel()
	depth, oldestAge, expiredLeases, err := monitor.snapshot(queryCtx)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		monitor.collectionErrors.Add(ctx, 1)
		slog.Warn(
			"transcription queue telemetry query failed",
			"error_type", safelog.ErrorType(err),
			"category", safelog.ErrorCategory(err),
		)
		return
	}
	if depth < 0 {
		depth = 0
	}
	if oldestAge < 0 {
		oldestAge = 0
	}
	if expiredLeases < 0 {
		expiredLeases = 0
	}
	monitor.depth.Record(ctx, depth)
	monitor.oldestAgeSeconds.Record(ctx, oldestAge.Seconds())
	monitor.expiredLeases.Record(ctx, expiredLeases)
}
