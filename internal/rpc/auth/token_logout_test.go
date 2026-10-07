package auth

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/openimsdk/open-im-server/v3/pkg/common/config"
	"github.com/openimsdk/open-im-server/v3/pkg/common/storage/controller"
	pbauth "github.com/openimsdk/protocol/auth"
	"github.com/openimsdk/protocol/constant"
	"github.com/openimsdk/protocol/msggateway"
	"github.com/openimsdk/tools/discovery"
	"github.com/openimsdk/tools/mcontext"
	"github.com/openimsdk/tools/tokenverify"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type logoutDB struct {
	controller.AuthDatabase
	states map[string]int
	writes int
}

func (d *logoutDB) GetTokensWithoutError(context.Context, string, int) (map[string]int, error) {
	return d.states, nil
}
func (d *logoutDB) SetTokenMapByUidPid(_ context.Context, _ string, _ int, states map[string]int) error {
	d.writes++
	for token, state := range states {
		d.states[token] = state
	}
	return nil
}

type logoutDiscovery struct {
	discovery.SvcDiscoveryRegistry
	conns []grpc.ClientConnInterface
}

func (d logoutDiscovery) GetConns(context.Context, string, ...grpc.DialOption) ([]grpc.ClientConnInterface, error) {
	return d.conns, nil
}

type logoutGateway struct {
	msggateway.UnimplementedMsgGatewayServer
	fail   atomic.Bool
	calls  atomic.Int32
	db     *logoutDB
	target string
}

func (g *logoutGateway) KickConnectionByToken(_ context.Context, req *msggateway.KickConnectionByTokenReq) (*msggateway.KickConnectionByTokenResp, error) {
	g.calls.Add(1)
	state, exists := g.db.states[req.Token]
	if req.Token != g.target || (exists && state == constant.NormalToken) {
		return nil, status.Error(codes.Internal, "token was not revoked first")
	}
	if g.fail.Load() {
		return nil, status.Error(codes.Unavailable, "gateway unavailable")
	}
	return &msggateway.KickConnectionByTokenResp{}, nil
}
func connectLogoutGateway(t *testing.T, gateway *logoutGateway) *grpc.ClientConn {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	msggateway.RegisterMsgGatewayServer(server, gateway)
	go server.Serve(listener)
	conn, err := grpc.NewClient("passthrough:///gateway", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(); server.Stop(); listener.Close() })
	return conn
}
func logoutToken(t *testing.T, user string, platform int, expired bool) string {
	t.Helper()
	claims := tokenverify.BuildClaims(user, platform, 7)
	if expired {
		claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Hour))
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestLogoutByTokenRevokesOnlyTargetAndRetriesAllGateways(t *testing.T) {
	target := logoutToken(t, "user", 5, false)
	newToken := logoutToken(t, "user", 5, false)
	db := &logoutDB{states: map[string]int{target: constant.NormalToken, newToken: constant.NormalToken}}
	first, second := &logoutGateway{db: db, target: target}, &logoutGateway{db: db, target: target}
	first.fail.Store(true)
	server := &authServer{authDatabase: db, config: &Config{Share: config.Share{Secret: "test-secret", IMAdminUserID: []string{"admin"}}}, RegisterCenter: logoutDiscovery{conns: []grpc.ClientConnInterface{connectLogoutGateway(t, first), connectLogoutGateway(t, second)}}}
	ctx := mcontext.SetOpUserID(t.Context(), "admin")
	req := &pbauth.ForceLogoutByTokenReq{UserID: "user", PlatformID: 5, Token: target}
	if _, err := server.ForceLogoutByToken(ctx, req); err == nil {
		t.Fatal("partial gateway failure must be retryable")
	}
	if first.calls.Load() != 1 || second.calls.Load() != 1 || db.states[newToken] != constant.NormalToken {
		t.Fatal("failure skipped another gateway or revoked the new token")
	}
	first.fail.Store(false)
	if _, err := server.ForceLogoutByToken(ctx, req); err != nil {
		t.Fatal(err)
	}
	if db.states[target] != constant.KickedToken || db.states[newToken] != constant.NormalToken {
		t.Fatal("retry changed the new token")
	}
	delete(db.states, target)
	if _, err := server.ForceLogoutByToken(ctx, req); err != nil {
		t.Fatal(err)
	}
	if second.calls.Load() != 3 {
		t.Fatal("missing token must still reach gateway")
	}
}

func TestLogoutByTokenValidatesAdminSignatureAndIdentity(t *testing.T) {
	db := &logoutDB{states: map[string]int{}}
	server := &authServer{authDatabase: db, config: &Config{Share: config.Share{Secret: "test-secret", IMAdminUserID: []string{"admin"}}}, RegisterCenter: logoutDiscovery{}}
	token := logoutToken(t, "user", 5, true)
	ctx := mcontext.SetOpUserID(t.Context(), "admin")
	for _, req := range []*pbauth.ForceLogoutByTokenReq{
		{UserID: "another", PlatformID: 5, Token: token},
		{UserID: "user", PlatformID: 1, Token: token},
		{UserID: "user", PlatformID: 5, Token: "invalid"},
	} {
		if _, err := server.ForceLogoutByToken(ctx, req); err == nil {
			t.Fatal("invalid token accepted")
		}
	}
	req := &pbauth.ForceLogoutByTokenReq{UserID: "user", PlatformID: 5, Token: token}
	if _, err := server.ForceLogoutByToken(t.Context(), req); err == nil {
		t.Fatal("non-admin accepted")
	}
	if _, err := server.ForceLogoutByToken(ctx, req); err != nil {
		t.Fatalf("expired signed token must still be disconnectable: %v", err)
	}
	if db.writes != 0 {
		t.Fatal("missing token must not recreate Redis state")
	}
}
