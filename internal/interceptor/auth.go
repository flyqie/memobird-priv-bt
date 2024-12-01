package interceptor

import (
	"connectrpc.com/connect"
	"context"
	"errors"
	"strings"
)

var AccessToken string

func NewAuthInterceptor() connect.UnaryInterceptorFunc {
	interceptor := func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(
			ctx context.Context,
			req connect.AnyRequest,
		) (connect.AnyResponse, error) {
			if !req.Spec().IsClient {
				if AccessToken != "" {
					authHeader := req.Header().Get("Authorization")
					if authHeader == "" {
						return nil, connect.NewError(
							connect.CodeUnauthenticated,
							errors.New("missing Authorization header"),
						)
					}
					if !strings.HasPrefix(authHeader, "Bearer ") {
						return nil, connect.NewError(
							connect.CodeUnauthenticated,
							errors.New("invalid Authorization header"),
						)
					}
					authToken := strings.TrimPrefix(authHeader, "Bearer ")
					if authToken != AccessToken {
						return nil, connect.NewError(
							connect.CodeUnauthenticated,
							errors.New("invalid token"),
						)
					}
				}
			}
			return next(ctx, req)
		}
	}
	return interceptor
}
