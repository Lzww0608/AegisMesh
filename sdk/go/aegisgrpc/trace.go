package aegisgrpc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strconv"

	tracepkg "github.com/aegismesh/aegismesh/pkg/trace"
	"google.golang.org/grpc/metadata"
)

const (
	traceLogEnv = "AEGIS_TRACE_LOG"

	traceIDMetadataKey = "x-aegis-trace-id"
	spanIDMetadataKey  = "x-aegis-span-id"
	attemptMetadataKey = "x-aegis-attempt"
)

// traceIDContextKey carries trace id context key state for resolver, picker, and reporter state.
type traceIDContextKey struct{}

// attemptContextKey carries attempt context key state for resolver, picker, and reporter state.
type attemptContextKey struct{}

// ContextWithTraceID provides the shared context with trace id helper for resolver, picker, and reporter state.
func ContextWithTraceID(ctx context.Context, traceID string) context.Context {
	if traceID == "" {
		return ctx
	}
	return context.WithValue(ctx, traceIDContextKey{}, traceID)
}

// ContextWithNewTraceID provides the shared context with new trace id helper for resolver, picker, and reporter state.
func ContextWithNewTraceID(ctx context.Context) context.Context {
	return ContextWithTraceID(ctx, newTraceID())
}

// TraceIDFromContext provides the shared trace id from context helper for resolver, picker, and reporter state.
func TraceIDFromContext(ctx context.Context) string {
	return traceIDFromContext(ctx)
}

// ensureTraceID provides the shared ensure trace id helper for resolver, picker, and reporter state.
func ensureTraceID(ctx context.Context) context.Context {
	if traceIDFromContext(ctx) != "" {
		return ctx
	}
	return ContextWithNewTraceID(ctx)
}

// traceIDFromContext provides the shared trace id from context helper for resolver, picker, and reporter state.
func traceIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	traceID, _ := ctx.Value(traceIDContextKey{}).(string)
	return traceID
}

// contextWithAttempt provides the shared context with attempt helper for resolver, picker, and reporter state.
func contextWithAttempt(ctx context.Context, attempt int) context.Context {
	if attempt <= 0 {
		attempt = 1
	}
	return context.WithValue(ctx, attemptContextKey{}, attempt)
}

// attemptFromContext provides the shared attempt from context helper for resolver, picker, and reporter state.
func attemptFromContext(ctx context.Context) int {
	if ctx == nil {
		return 1
	}
	attempt, _ := ctx.Value(attemptContextKey{}).(int)
	if attempt <= 0 {
		return 1
	}
	return attempt
}

// contextWithTraceMetadata provides the shared context with trace metadata helper for resolver, picker, and reporter state.
func contextWithTraceMetadata(ctx context.Context, spanID string) context.Context {
	traceID := traceIDFromContext(ctx)
	if traceID == "" {
		ctx = ensureTraceID(ctx)
		traceID = traceIDFromContext(ctx)
	}
	attempt := strconv.Itoa(attemptFromContext(ctx))
	return metadata.AppendToOutgoingContext(ctx,
		traceIDMetadataKey, traceID,
		spanIDMetadataKey, spanID,
		attemptMetadataKey, attempt,
	)
}

// traceWriterFromOptions provides the shared trace writer from options helper for resolver, picker, and reporter state.
func traceWriterFromOptions(options DialOptions) (tracepkg.Writer, error) {
	path := options.TraceLogPath
	if path == "" {
		path = os.Getenv(traceLogEnv)
	}
	if path == "" {
		return nil, nil
	}
	return tracepkg.NewDefaultAsyncJSONLWriter(path)
}

// newTraceID initializes trace id with package defaults for this package's call path.
func newTraceID() string {
	return "trace-" + randomHex(16)
}

// newSpanID initializes span id with package defaults for this package's call path.
func newSpanID() string {
	return "span-" + randomHex(8)
}

// randomHex keeps random hex rules consistent for resolver, picker, and reporter state.
func randomHex(bytesLen int) string {
	buf := make([]byte, bytesLen)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(int64(os.Getpid()), 10)
	}
	return hex.EncodeToString(buf)
}
