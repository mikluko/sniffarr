// Package auth authorizes connect requests by a bearer token.
package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"connectrpc.com/connect"
)

var errUnauthenticated = errors.New("missing or wrong bearer token")

// Bearer is a server-side interceptor that answers UNAUTHENTICATED to any
// request whose Authorization header does not carry the token.
type Bearer struct {
	token []byte
}

// NewBearer returns a Bearer for the token, which must not be empty.
func NewBearer(token string) *Bearer {
	if token == "" {
		panic("auth: empty token")
	}
	return &Bearer{token: []byte(token)}
}

func (b *Bearer) check(h http.Header) error {
	got, ok := strings.CutPrefix(h.Get("Authorization"), "Bearer ")
	if !ok || subtle.ConstantTimeCompare([]byte(got), b.token) != 1 {
		return connect.NewError(connect.CodeUnauthenticated, errUnauthenticated)
	}
	return nil
}

func (b *Bearer) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if req.Spec().IsClient {
			return next(ctx, req)
		}
		if err := b.check(req.Header()); err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

func (b *Bearer) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (b *Bearer) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if err := b.check(conn.RequestHeader()); err != nil {
			return err
		}
		return next(ctx, conn)
	}
}

// Client is a client-side interceptor that sends the token as a bearer
// token.
type Client string

func (c Client) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		req.Header().Set("Authorization", "Bearer "+string(c))
		return next(ctx, req)
	}
}

func (c Client) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		conn.RequestHeader().Set("Authorization", "Bearer "+string(c))
		return conn
	}
}

func (c Client) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}
