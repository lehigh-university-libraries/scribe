package server

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
	"github.com/lehigh-university-libraries/scribe/proto/scribe/v1/scribev1connect"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestConnectTelemetryRecordsBoundedMetricsAndRedactedTrace(t *testing.T) {
	const sensitive = "PRIVATE_PROVIDER_BODY_token=secret"
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = meterProvider.Shutdown(context.Background()) })
	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(spanRecorder),
	)
	t.Cleanup(func() { _ = tracerProvider.Shutdown(context.Background()) })
	interceptor, err := newConnectTelemetryInterceptor(
		meterProvider.Meter("test"),
		tracerProvider.Tracer("test"),
	)
	if err != nil {
		t.Fatalf("newConnectTelemetryInterceptor: %v", err)
	}

	request := connect.NewRequest(&scribev1.GetItemRequest{})
	request.Header().Set("Traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	handler := interceptor.WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New(sensitive))
	})
	if _, err := handler(context.Background(), request); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("handler code = %v, want invalid_argument", connect.CodeOf(err))
	}

	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &metrics); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	requestMetric := findConnectMetric(t, metrics, "scribe.connect.server.requests")
	requestSum, ok := requestMetric.Data.(metricdata.Sum[int64])
	if !ok || len(requestSum.DataPoints) != 1 || requestSum.DataPoints[0].Value != 1 {
		t.Fatalf("request metric = %#v", requestMetric.Data)
	}
	labels := requestSum.DataPoints[0].Attributes.ToSlice()
	if len(labels) != 3 || !hasAttribute(labels, "rpc.service", "unknown") ||
		!hasAttribute(labels, "rpc.method", "unknown") ||
		!hasAttribute(labels, "rpc.connect.status_code", "invalid_argument") {
		t.Fatalf("request metric labels = %v", labels)
	}
	durationMetric := findConnectMetric(t, metrics, "scribe.connect.server.duration")
	durationHistogram, ok := durationMetric.Data.(metricdata.Histogram[float64])
	if !ok || len(durationHistogram.DataPoints) != 1 || durationHistogram.DataPoints[0].Count != 1 {
		t.Fatalf("duration metric = %#v", durationMetric.Data)
	}
	if !slices.Equal(durationHistogram.DataPoints[0].Bounds, connectDurationBoundaries) {
		t.Fatalf("duration bounds = %v, want %v", durationHistogram.DataPoints[0].Bounds, connectDurationBoundaries)
	}

	spans := spanRecorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	span := spans[0]
	if span.Name() != "unknown" || span.SpanKind() != trace.SpanKindServer {
		t.Fatalf("span name/kind = %q/%v", span.Name(), span.SpanKind())
	}
	if span.Parent().IsValid() {
		t.Fatalf("public server span retained attacker-controlled parent: %v", span.Parent())
	}
	if got := span.SpanContext().TraceID().String(); got == "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("public server span reused attacker-controlled trace ID %q", got)
	}
	if span.Status().Code != codes.Error || span.Status().Description != "invalid_argument" {
		t.Fatalf("span status = %+v", span.Status())
	}
	if attributesContain(span.Attributes(), sensitive) || attributesContain(span.Attributes(), "secret") {
		t.Fatalf("span attributes exposed error details: %v", span.Attributes())
	}
}

func TestBoundedConnectProcedureUsesCompiledDescriptors(t *testing.T) {
	t.Parallel()

	known := boundedConnectProcedure(scribev1connect.ItemServiceGetItemProcedure)
	if known.name != scribev1connect.ItemServiceGetItemProcedure || known.service != "scribe.v1.ItemService" || known.method != "GetItem" {
		t.Fatalf("known procedure = %+v", known)
	}
	for _, raw := range []string{
		"/scribe.v1.ItemService/AttackerChosenMethod",
		"/other.v1.ItemService/GetItem",
		"/scribe.v1.ItemService/GetItem/arbitrary",
		strings.Repeat("x", 1024),
	} {
		if got := boundedConnectProcedure(raw); got.name != "unknown" || got.service != "unknown" || got.method != "unknown" {
			t.Fatalf("boundedConnectProcedure(%q) = %+v", raw, got)
		}
	}
}

func findConnectMetric(t *testing.T, resourceMetrics metricdata.ResourceMetrics, name string) metricdata.Metrics {
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

func hasAttribute(attributes []attribute.KeyValue, key, value string) bool {
	for _, candidate := range attributes {
		if string(candidate.Key) == key && candidate.Value.AsString() == value {
			return true
		}
	}
	return false
}

func attributesContain(attributes []attribute.KeyValue, value string) bool {
	for _, candidate := range attributes {
		if strings.Contains(string(candidate.Key), value) || strings.Contains(fmt.Sprint(candidate.Value.AsInterface()), value) {
			return true
		}
	}
	return false
}
