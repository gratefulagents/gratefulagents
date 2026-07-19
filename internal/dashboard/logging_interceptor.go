package dashboard

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

var rpcLog = logf.Log.WithName("dashboard.rpc")

// LoggingInterceptor returns an interceptor that logs every incoming RPC:
// procedure, actor, duration, and outcome. Failures are logged with the
// Connect error code and message so they are visible server-side instead of
// only surfacing as opaque client toasts.
func LoggingInterceptor() connect.Interceptor {
	return loggingInterceptor{}
}

type loggingInterceptor struct{}

func (loggingInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if req.Spec().IsClient {
			return next(ctx, req)
		}
		start := time.Now()
		res, err := next(ctx, req)
		logRPC(ctx, req.Spec().Procedure, req.Peer().Addr, start, err)
		return res, err
	}
}

func (loggingInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (loggingInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		start := time.Now()
		err := next(ctx, conn)
		logRPC(ctx, conn.Spec().Procedure, conn.Peer().Addr, start, err)
		return err
	}
}

func logRPC(ctx context.Context, procedure, peer string, start time.Time, err error) {
	fields := []any{
		"procedure", procedure,
		"peer", peer,
		"duration", time.Since(start).Round(time.Millisecond).String(),
	}
	if actor, ok := requestActorFromContextOK(ctx); ok && actor.Subject != "" {
		fields = append(fields, "actor", actor.Subject)
	}
	if err == nil {
		rpcLog.Info("rpc ok", fields...)
		return
	}
	// Client cancellations (stream teardown, page navigation) are routine.
	code := connect.CodeOf(err)
	fields = append(fields, "code", code.String(), "error", err.Error())
	if code == connect.CodeCanceled || errors.Is(err, context.Canceled) {
		rpcLog.V(1).Info("rpc canceled", fields...)
		return
	}
	rpcLog.Info("rpc failed", fields...)
}
