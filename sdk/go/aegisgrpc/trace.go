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

type traceIDContextKey struct{}
type attemptContextKey struct{}

func ContextWithTraceID(ctx context.Context, traceID string) context.Context {
	if traceID == "" {
		return ctx
	}
	return context.WithValue(ctx, traceIDContextKey{}, traceID)
}

func ContextWithNewTraceID(ctx context.Context) context.Context {
	return ContextWithTraceID(ctx, newTraceID())
}

func TraceIDFromContext(ctx context.Context) string {
	return traceIDFromContext(ctx)
}

func ensureTraceID(ctx context.Context) context.Context {
	if traceIDFromContext(ctx) != "" {
		return ctx
	}
	return ContextWithNewTraceID(ctx)
}

func traceIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	traceID, _ := ctx.Value(traceIDContextKey{}).(string)
	return traceID
}

func contextWithAttempt(ctx context.Context, attempt int) context.Context {
	if attempt <= 0 {
		attempt = 1
	}
	return context.WithValue(ctx, attemptContextKey{}, attempt)
}

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

func traceWriterFromOptions(options DialOptions) (*tracepkg.JSONLWriter, error) {
	path := options.TraceLogPath
	if path == "" {
		path = os.Getenv(traceLogEnv)
	}
	if path == "" {
		return nil, nil
	}
	return tracepkg.NewJSONLWriter(path)
}

func newTraceID() string {
	return "trace-" + randomHex(16)
}

func newSpanID() string {
	return "span-" + randomHex(8)
}

func randomHex(bytesLen int) string {
	buf := make([]byte, bytesLen)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(int64(os.Getpid()), 10)
	}
	return hex.EncodeToString(buf)
}
