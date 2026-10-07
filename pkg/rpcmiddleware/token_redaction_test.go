package rpcmiddleware

import (
	"strings"
	"testing"

	"github.com/openimsdk/protocol/auth"
	"github.com/openimsdk/protocol/msggateway"
	"github.com/openimsdk/tools/errs"
)

func TestTokenRedactionDoesNotChangeWireRequest(t *testing.T) {
	for _, req := range []any{
		&auth.ForceLogoutByTokenReq{UserID: "user", PlatformID: 5, Token: "private-token"},
		&msggateway.KickConnectionByTokenReq{UserID: "user", PlatformID: 5, Token: "private-token"},
		&auth.ParseTokenReq{Token: "private-token"},
	} {
		redacted, token := redactedRequest(req)
		if token != "private-token" || redacted == req || strings.Contains(redacted.(interface{ String() string }).String(), token) {
			t.Fatal("redaction leaked or mutated token")
		}
		if !strings.Contains(req.(interface{ String() string }).String(), token) {
			t.Fatal("wire request lost token")
		}
	}
	err := redactError(errs.ErrArgs.WrapMsg("invalid", "token", "private-token"), "private-token")
	if strings.Contains(err.Error(), "private-token") {
		t.Fatal("error leaked token")
	}
}
