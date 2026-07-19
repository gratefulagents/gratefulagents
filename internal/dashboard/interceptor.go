package dashboard

import (
	"context"

	"connectrpc.com/connect"
)

// requestActorInterceptor populates the requestActor context value for every
// incoming RPC by extracting JWT claims (enterprise) or fallback headers.
type requestActorInterceptor struct{}

// RequestActorInterceptor returns an interceptor that extracts the actor
// identity from every incoming request and places it on the context.
func RequestActorInterceptor() connect.Interceptor {
	return requestActorInterceptor{}
}

func (requestActorInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if req.Spec().IsClient {
			return next(ctx, req)
		}
		ctx = withRequestActor(ctx, req.Header())
		return next(ctx, req)
	}
}

func (requestActorInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (requestActorInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		ctx = withRequestActor(ctx, conn.RequestHeader())
		return next(ctx, conn)
	}
}
