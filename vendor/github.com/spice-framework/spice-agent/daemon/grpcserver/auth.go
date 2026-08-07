// Package grpcserver implements the authenticated local gRPC process boundary
// without adding transport dependencies to the daemon lifecycle core.
package grpcserver

import (
	"context"
	"encoding/base64"
	"errors"
	"io"

	"github.com/spice-framework/spice-agent/daemon/endpoint"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	// AuthenticationMetadataKey is the standard gRPC metadata key carrying the
	// user-local daemon bearer credential.
	AuthenticationMetadataKey = "authorization"

	endpointTokenBytes            = endpoint.TokenBytes
	endpointTokenAttempts         = 4
	endpointBearerPrefix          = endpoint.BearerPrefix
	endpointAuthenticationFailure = "local daemon authentication failed"
)

// EndpointToken is retained as a source-compatible alias for the shared
// transport-neutral endpoint credential.
type EndpointToken = endpoint.Token

// GenerateEndpointToken creates a credential from the operating-system CSPRNG.
func GenerateEndpointToken() (EndpointToken, error) { return endpoint.GenerateToken() }

func generateEndpointToken(random io.Reader) (EndpointToken, error) {
	if random == nil {
		return EndpointToken{}, errors.New("endpoint token randomness is nil")
	}
	for range endpointTokenAttempts {
		var raw [endpointTokenBytes]byte
		if _, err := io.ReadFull(random, raw[:]); err != nil {
			return EndpointToken{}, errors.New("generate endpoint token")
		}
		token, err := endpoint.ParseToken(base64.RawURLEncoding.EncodeToString(raw[:]))
		if err == nil {
			return token, nil
		}
	}
	return EndpointToken{}, errors.New("generate nonzero endpoint token")
}

// ParseEndpointToken decodes the canonical unpadded base64url credential form.
func ParseEndpointToken(encoded string) (EndpointToken, error) {
	return endpoint.ParseToken(encoded)
}

type transportAuthenticationKey struct{}

func transportAuthenticated(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	authenticated, _ := ctx.Value(transportAuthenticationKey{}).(bool)
	return authenticated
}

// newAuthenticationInterceptors constructs matching unary and streaming
// middleware. It remains private so the eventual server constructor can make
// installing both paths mandatory. Authentication happens after gRPC framing
// and decode but before an application handler may inspect daemon state.
func newAuthenticationInterceptors(
	token EndpointToken,
) (grpc.UnaryServerInterceptor, grpc.StreamServerInterceptor, error) {
	if token.Validate() != nil {
		return nil, nil, errors.New("endpoint authentication token is invalid")
	}
	unary := func(
		ctx context.Context,
		request any,
		_ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		authenticated, err := authenticateTransportContext(ctx, token)
		if err != nil {
			return nil, err
		}
		return handler(authenticated, request)
	}
	stream := func(
		service any,
		server grpc.ServerStream,
		_ *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		if server == nil {
			return unauthenticatedTransport()
		}
		authenticated, err := authenticateTransportContext(server.Context(), token)
		if err != nil {
			return err
		}
		return handler(service, authenticatedServerStream{ServerStream: server, ctx: authenticated})
	}
	return unary, stream, nil
}

func authenticateTransportContext(ctx context.Context, expected EndpointToken) (context.Context, error) {
	if ctx == nil {
		return nil, unauthenticatedTransport()
	}
	values, present := metadata.FromIncomingContext(ctx)
	if !present {
		return nil, unauthenticatedTransport()
	}
	authorization := values.Get(AuthenticationMetadataKey)
	if len(authorization) != 1 {
		return nil, unauthenticatedTransport()
	}
	presented, err := parseBearerToken(authorization[0])
	if err != nil || !presented.Equal(expected) {
		return nil, unauthenticatedTransport()
	}
	return context.WithValue(ctx, transportAuthenticationKey{}, true), nil
}

func parseBearerToken(value string) (EndpointToken, error) {
	return endpoint.ParseAuthorizationValue(value)
}

func unauthenticatedTransport() error {
	return status.Error(codes.Unauthenticated, endpointAuthenticationFailure)
}

type authenticatedServerStream struct {
	grpc.ServerStream
	ctx context.Context //nolint:containedctx // immutable wrapper replaces only the authenticated stream context.
}

func (stream authenticatedServerStream) Context() context.Context { return stream.ctx }
