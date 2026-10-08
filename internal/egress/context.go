package egress

import "context"

// routeKey carries the matched route from ServeHTTP to the reverse proxy's rewrite.
type routeKey struct{}

func withRoute(ctx context.Context, r *Route) context.Context {
	return context.WithValue(ctx, routeKey{}, r)
}

func routeFrom(ctx context.Context) *Route {
	return ctx.Value(routeKey{}).(*Route)
}
