package auth

import (
	"context"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/openimsdk/open-im-server/v3/pkg/authverify"
	pbauth "github.com/openimsdk/protocol/auth"
	"github.com/openimsdk/protocol/constant"
	"github.com/openimsdk/protocol/msggateway"
	"github.com/openimsdk/tools/errs"
	"github.com/openimsdk/tools/tokenverify"
)

// Validate the signed identity even for expired or previously revoked tokens.
// ParseToken cannot serve this purpose: retries still need to disconnect them.
func validateLogoutToken(secret string, req *pbauth.ForceLogoutByTokenReq) error {
	if err := req.Check(); err != nil {
		return errs.ErrArgs.WrapMsg("invalid token logout request")
	}
	claims := new(tokenverify.Claims)
	token, err := jwt.NewParser(jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithoutClaimsValidation()).ParseWithClaims(req.Token, claims, authverify.Secret(secret))
	if err != nil || !token.Valid || claims.UserID != req.UserID || claims.PlatformID != int(req.PlatformID) {
		return errs.ErrArgs.WrapMsg("target token signature or identity is invalid")
	}
	return nil
}

func (s *authServer) ForceLogoutByToken(ctx context.Context, req *pbauth.ForceLogoutByTokenReq) (*pbauth.ForceLogoutByTokenResp, error) {
	if err := authverify.CheckAdmin(ctx, s.config.Share.IMAdminUserID); err != nil {
		return nil, err
	}
	if err := validateLogoutToken(s.config.Share.Secret, req); err != nil {
		return nil, err
	}
	if req.PlatformID == constant.AdminPlatformID || authverify.IsManagerUserID(req.UserID, s.config.Share.IMAdminUserID) {
		return nil, errs.ErrNoPermission.WrapMsg("cannot revoke admin token")
	}
	states, err := s.authDatabase.GetTokensWithoutError(ctx, req.UserID, int(req.PlatformID))
	if err != nil {
		return nil, err
	}
	if _, exists := states[req.Token]; exists {
		// Write just the target hash field. Writing the snapshot of every token
		// could overwrite a concurrent login or resurrect another revoked token.
		if err := s.authDatabase.SetTokenMapByUidPid(ctx, req.UserID, int(req.PlatformID), map[string]int{req.Token: constant.KickedToken}); err != nil {
			return nil, err
		}
	}
	conns, err := s.RegisterCenter.GetConns(ctx, s.config.Share.RpcRegisterName.MessageGateway)
	if err != nil {
		return nil, err
	}
	results := make(chan error, len(conns))
	for _, conn := range conns {
		go func() {
			attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			_, err := msggateway.NewMsgGatewayClient(conn).KickConnectionByToken(attempt, &msggateway.KickConnectionByTokenReq{
				UserID: req.UserID, PlatformID: req.PlatformID, Token: req.Token,
			})
			results <- err
		}()
	}
	failed := false
	for range conns {
		if <-results != nil {
			failed = true
		}
	}
	if failed {
		return nil, errs.ErrInternalServer.WrapMsg("token revoked; gateway disconnect incomplete, retry required")
	}
	return &pbauth.ForceLogoutByTokenResp{}, nil
}
