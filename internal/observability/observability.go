package observability

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"actrail/internal/config"
	"go.opentelemetry.io/contrib/bridges/otelzap"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	otellog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	serviceName        = "actrail-server"
	instrumentation    = "actrail"
	defaultLogFilename = "actrail-server.log"
)

type ShutdownFunc func(context.Context) error

type Config struct {
	Endpoint string
	Protocol string
	Insecure bool
	LogPath  string
}

func FromEnv(storage config.Storage) Config {
	endpoint := strings.TrimSpace(os.Getenv("ACTRAIL_OTEL_ENDPOINT"))
	return Config{
		Endpoint: firstNonEmpty(endpoint, strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))),
		Protocol: strings.TrimSpace(os.Getenv("ACTRAIL_OTEL_PROTOCOL")),
		Insecure: envBool("ACTRAIL_OTEL_INSECURE", true),
		LogPath:  firstNonEmpty(strings.TrimSpace(os.Getenv("ACTRAIL_LOG_PATH")), filepath.Join(storage.DataDir, "log", defaultLogFilename)),
	}
}

func Setup(ctx context.Context, cfg Config) (*zap.Logger, ShutdownFunc, error) {
	logger, logFile, err := newZapLogger(cfg.LogPath, nil)
	if err != nil {
		return nil, nil, err
	}
	shutdowns := []ShutdownFunc{func(context.Context) error { return logger.Sync() }}
	if logFile != nil {
		shutdowns = append(shutdowns, func(context.Context) error { return logFile.Close() })
	}

	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		return logger, joinShutdowns(shutdowns...), nil
	}
	otelCfg, err := parseOTLPEndpoint(endpoint, cfg.Insecure)
	if err != nil {
		_ = logger.Sync()
		if logFile != nil {
			_ = logFile.Close()
		}
		return nil, nil, err
	}
	res, err := resource.New(ctx, resource.WithAttributes(
		semconv.ServiceNameKey.String(serviceName),
		attribute.String("telemetry.sdk.language", "go"),
	))
	if err != nil {
		_ = logger.Sync()
		if logFile != nil {
			_ = logFile.Close()
		}
		return nil, nil, fmt.Errorf("create otel resource: %w", err)
	}

	traceExporter, err := otlptracegrpc.New(ctx, otelCfg.traceOptions()...)
	if err != nil {
		return nil, nil, fmt.Errorf("create otel trace exporter: %w", err)
	}
	tracerProvider := trace.NewTracerProvider(
		trace.WithBatcher(traceExporter),
		trace.WithResource(res),
	)
	otel.SetTracerProvider(tracerProvider)
	shutdowns = append(shutdowns, tracerProvider.Shutdown)

	metricExporter, err := otlpmetricgrpc.New(ctx, otelCfg.metricOptions()...)
	if err != nil {
		_ = joinShutdowns(shutdowns...)(ctx)
		return nil, nil, fmt.Errorf("create otel metric exporter: %w", err)
	}
	meterProvider := metric.NewMeterProvider(
		metric.WithResource(res),
		metric.WithReader(metric.NewPeriodicReader(metricExporter)),
	)
	otel.SetMeterProvider(meterProvider)
	shutdowns = append(shutdowns, meterProvider.Shutdown)

	logExporter, err := otlploggrpc.New(ctx, otelCfg.logOptions()...)
	if err != nil {
		_ = joinShutdowns(shutdowns...)(ctx)
		return nil, nil, fmt.Errorf("create otel log exporter: %w", err)
	}
	loggerProvider := otellog.NewLoggerProvider(
		otellog.WithResource(res),
		otellog.WithProcessor(otellog.NewBatchProcessor(logExporter)),
	)
	logger = logger.WithOptions(zap.WrapCore(func(core zapcore.Core) zapcore.Core {
		return zapcore.NewTee(core, otelzap.NewCore(instrumentation, otelzap.WithLoggerProvider(loggerProvider)))
	}))
	shutdowns[0] = func(context.Context) error { return logger.Sync() }
	shutdowns = append(shutdowns, loggerProvider.Shutdown)

	otel.SetTextMapPropagator(propagation.TraceContext{})
	return logger, joinShutdowns(shutdowns...), nil
}

func Handler(name string, handler http.Handler) http.Handler {
	return otelhttp.NewHandler(handler, name)
}

func newZapLogger(logPath string, extra zapcore.Core) (*zap.Logger, *os.File, error) {
	if strings.TrimSpace(logPath) == "" {
		return nil, nil, fmt.Errorf("log path required")
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, nil, fmt.Errorf("create log dir: %w", err)
	}
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("open log file: %w", err)
	}
	encCfg := zap.NewProductionEncoderConfig()
	encCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	level := zapcore.InfoLevel
	cores := []zapcore.Core{
		zapcore.NewCore(zapcore.NewJSONEncoder(encCfg), zapcore.AddSync(file), level),
		zapcore.NewCore(zapcore.NewJSONEncoder(encCfg), zapcore.AddSync(os.Stdout), level),
	}
	if extra != nil {
		cores = append(cores, extra)
	}
	logger := zap.New(zapcore.NewTee(cores...), zap.AddCaller(), zap.ErrorOutput(zapcore.AddSync(os.Stderr)))
	return logger, file, nil
}

type otlpEndpoint struct {
	endpoint string
	insecure bool
}

func parseOTLPEndpoint(raw string, fallbackInsecure bool) (otlpEndpoint, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return otlpEndpoint{}, fmt.Errorf("otel endpoint required")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return otlpEndpoint{endpoint: value, insecure: fallbackInsecure}, nil
	}
	return otlpEndpoint{endpoint: parsed.Host, insecure: parsed.Scheme == "http" || fallbackInsecure}, nil
}

func (e otlpEndpoint) traceOptions() []otlptracegrpc.Option {
	opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(e.endpoint)}
	if e.insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}
	return opts
}

func (e otlpEndpoint) metricOptions() []otlpmetricgrpc.Option {
	opts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(e.endpoint)}
	if e.insecure {
		opts = append(opts, otlpmetricgrpc.WithInsecure())
	}
	return opts
}

func (e otlpEndpoint) logOptions() []otlploggrpc.Option {
	opts := []otlploggrpc.Option{otlploggrpc.WithEndpoint(e.endpoint)}
	if e.insecure {
		opts = append(opts, otlploggrpc.WithInsecure())
	}
	return opts
}

func joinShutdowns(shutdowns ...ShutdownFunc) ShutdownFunc {
	return func(ctx context.Context) error {
		var err error
		for i := len(shutdowns) - 1; i >= 0; i-- {
			if shutdowns[i] == nil {
				continue
			}
			err = errors.Join(err, shutdowns[i](ctx))
		}
		return err
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	switch strings.ToLower(value) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}
