package inventory

import "context"

type SkipNativeFTSEnqueueCtx struct{}

func WithSkipNativeFTSEnqueue(ctx context.Context, skip bool) context.Context {
	if !skip {
		return ctx
	}

	return context.WithValue(ctx, SkipNativeFTSEnqueueCtx{}, true)
}

func SkipNativeFTSEnqueueFromContext(ctx context.Context) bool {
	skip, _ := ctx.Value(SkipNativeFTSEnqueueCtx{}).(bool)
	return skip
}
