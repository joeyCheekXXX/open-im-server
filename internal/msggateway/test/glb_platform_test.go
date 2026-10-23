package test

import (
	"github.com/openimsdk/open-im-server/v3/internal/msggateway"
	"net/http/httptest"
	"testing"
)

func TestGLBConnectionArgumentsAcceptIndependentPlatform(t *testing.T) {
	conn := &msggateway.UserConnContext{Req: httptest.NewRequest("GET", "/?sendID=account&token=credential&platformID=13&operationID=glb-test&isBackground=false", nil)}
	if err := conn.ParseEssentialArgs(); err != nil {
		t.Fatalf("GLB connection rejected: %v", err)
	}
	if conn.GetPlatformID() != 13 || conn.GetUserID() != "account" {
		t.Fatal("GLB connection lost its identity")
	}
}
