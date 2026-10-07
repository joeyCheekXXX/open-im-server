package rpcmiddleware

import (
	"context"
	"errors"
	"strings"

	"github.com/openimsdk/protocol/auth"
	"github.com/openimsdk/protocol/msggateway"
	"github.com/openimsdk/tools/errs"
	"github.com/openimsdk/tools/mw"
	"google.golang.org/grpc"
)

func redactedRequest(req any) (any, string) {
	switch r := req.(type) {
	case *auth.ForceLogoutByTokenReq:
		return &auth.ForceLogoutByTokenReq{UserID: r.UserID, PlatformID: r.PlatformID, Token: "[REDACTED]"}, r.Token
	case *msggateway.KickConnectionByTokenReq:
		return &msggateway.KickConnectionByTokenReq{UserID: r.UserID, PlatformID: r.PlatformID, Token: "[REDACTED]"}, r.Token
	case *auth.ParseTokenReq:
		return &auth.ParseTokenReq{Token: "[REDACTED]"}, r.Token
	default:
		return req, ""
	}
}

func redactError(err error, token string) error {
	if err == nil || token == "" || !strings.Contains(err.Error(), token) {
		return err
	}
	var code errs.CodeError
	if errors.As(err, &code) {
		return errs.NewCodeError(code.Code(), strings.ReplaceAll(code.Msg(), token, "[REDACTED]")).WithDetail(strings.ReplaceAll(err.Error(), token, "[REDACTED]"))
	}
	return errs.ErrInternalServer.WrapMsg("token request failed")
}

// GrpcServer preserves upstream validation, metadata and error handling while
// giving its request logger a separate redacted object.
func GrpcServer() grpc.ServerOption {
	return grpc.ChainUnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		redacted, token := redactedRequest(req)
		return mw.RpcServerInterceptor(ctx, redacted, info, func(ctx context.Context, _ any) (any, error) {
			resp, err := handler(ctx, req)
			return resp, redactError(err, token)
		})
	})
}

func GrpcClient() grpc.DialOption {
	return grpc.WithChainUnaryInterceptor(func(ctx context.Context, method string, req, resp any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		redacted, _ := redactedRequest(req)
		return mw.RpcClientInterceptor(ctx, method, redacted, resp, cc, func(ctx context.Context, method string, _, resp any, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
			return invoker(ctx, method, req, resp, cc, opts...)
		}, opts...)
	})
}
