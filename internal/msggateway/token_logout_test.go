package msggateway

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openimsdk/open-im-server/v3/pkg/common/config"
	"github.com/openimsdk/open-im-server/v3/pkg/rpcli"
	pbauth "github.com/openimsdk/protocol/auth"
	"google.golang.org/grpc"
)

type logoutConn struct {
	closed      atomic.Bool
	done        chan struct{}
	readStarted chan struct{}
	readRelease chan struct{}
	writes      atomic.Int32
}

func (c *logoutConn) ReadMessage() ([]byte, error) {
	if c.readStarted != nil {
		close(c.readStarted)
	}
	<-c.done
	if c.readRelease != nil {
		<-c.readRelease
	}
	return nil, errors.New("closed")
}

func (c *logoutConn) WriteMessage([]byte) error { c.writes.Add(1); return nil }
func (c *logoutConn) Close() error {
	if c.closed.CompareAndSwap(false, true) {
		close(c.done)
	}
	return nil
}

type registrationAuth struct {
	grpc.ClientConnInterface
	valid   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (a *registrationAuth) Invoke(ctx context.Context, _ string, _ any, reply any, _ ...grpc.CallOption) error {
	if a.entered != nil {
		close(a.entered)
		select {
		case <-a.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if !a.valid.Load() {
		return errors.New("revoked")
	}
	resp := reply.(*pbauth.ParseTokenResp)
	resp.UserID, resp.PlatformID = "user", 5
	return nil
}
func testLogoutClient(ws *WsServer, userID string, platformID int, token string) (*Client, *logoutConn) {
	ctx := newTempContext()
	ctx.info.UserID, ctx.info.PlatformID, ctx.info.Token = userID, platformID, token
	conn := &logoutConn{done: make(chan struct{})}
	client := new(Client)
	client.ResetClient(ctx, conn, ws)
	return client, conn
}
func startLogoutEvents(t *testing.T, ws *WsServer) {
	t.Helper()
	stop, finished := make(chan struct{}), make(chan struct{})
	go func() { defer close(finished); ws.runClientEvents(stop) }()
	t.Cleanup(func() { close(stop); <-finished })
}

func TestTokenKickDisconnectsAllMatchesAndPreservesNewLogin(t *testing.T) {
	ws := NewWsServer(&Config{})
	var matching, unrelated []*logoutConn
	for _, spec := range []struct {
		user     string
		platform int
		token    string
		match    bool
	}{
		{"user", 5, "old-token", true}, {"user", 5, "old-token", true},
		{"user", 5, "new-token", false}, {"user", 1, "old-token", false}, {"other", 5, "old-token", false},
	} {
		client, conn := testLogoutClient(ws, spec.user, spec.platform, spec.token)
		ws.clients.Set(spec.user, client)
		if spec.match {
			matching = append(matching, conn)
		} else {
			unrelated = append(unrelated, conn)
		}
	}
	startLogoutEvents(t, ws)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	for i := 0; i < 2; i++ {
		if err := ws.KickConnectionByToken(ctx, "user", 5, "old-token"); err != nil {
			t.Fatal(err)
		}
	}
	for _, conn := range matching {
		if !conn.closed.Load() || conn.writes.Load() != 1 {
			t.Fatal("target was not notified and closed exactly once")
		}
	}
	for _, conn := range unrelated {
		if conn.closed.Load() || conn.writes.Load() != 0 {
			t.Fatal("another token, user or platform was kicked")
		}
	}
}

func TestRegistrationRechecksTokenBeforePublishingSuccessOrReading(t *testing.T) {
	ws := NewWsServer(&Config{Discovery: config.Discovery{Enable: config.KUBERNETES}})
	auth := &registrationAuth{entered: make(chan struct{}), release: make(chan struct{})}
	auth.valid.Store(true) // handshake already passed
	ws.authClient = rpcli.NewAuthClient(auth)
	client, conn := testLogoutClient(ws, "user", 5, "old-token")
	var ready atomic.Bool
	request := &clientRegistration{client: client, done: make(chan error, 1), ready: func() error { ready.Store(true); return nil }}
	startLogoutEvents(t, ws)
	ws.registerChan <- request
	<-auth.entered
	auth.valid.Store(false) // revoke between handshake and registration
	close(auth.release)
	if err := <-request.done; err == nil {
		t.Fatal("late revoked connection registered")
	}
	if _, exists := ws.clients.GetAll("user"); exists || ready.Load() {
		t.Fatal("revoked registration was published")
	}
	client.hbCancel()
	conn.Close()
}

func TestDelayedRemoteLoginCannotKickNewToken(t *testing.T) {
	ws := NewWsServer(&Config{})
	auth := new(registrationAuth) // revoked incoming token
	ws.authClient = rpcli.NewAuthClient(auth)
	newClient, conn := testLogoutClient(ws, "user", 5, "new-token")
	ws.clients.Set("user", newClient)
	oldClient, _ := testLogoutClient(ws, "user", 5, "old-token")
	ws.checkRemoteLogin(oldClient)
	if conn.closed.Load() {
		t.Fatal("delayed old login kicked new token")
	}
	newClient.hbCancel()
	oldClient.hbCancel()
}

func TestUnregisterRemovesOnlyItsConnectionWhenAddressesMatch(t *testing.T) {
	ws := NewWsServer(&Config{})
	oldClient, _ := testLogoutClient(ws, "user", 5, "old-token")
	newClient, _ := testLogoutClient(ws, "user", 5, "new-token")
	oldClient.ctx.RemoteAddr, newClient.ctx.RemoteAddr = "same-address", "same-address"
	ws.clients.Set("user", oldClient)
	ws.clients.Set("user", newClient)
	ws.clients.DeleteClients("user", []*Client{oldClient})
	clients, _, _ := ws.clients.Get("user", 5)
	if len(clients) != 1 || clients[0] != newClient {
		t.Fatal("old cleanup removed a new connection")
	}
	oldClient.hbCancel()
	newClient.hbCancel()
}

func TestKickDoesNotRecycleClientWhileReaderIsRunning(t *testing.T) {
	ws := NewWsServer(&Config{})
	client, conn := testLogoutClient(ws, "user", 5, "old-token")
	conn.readStarted, conn.readRelease = make(chan struct{}), make(chan struct{})
	go client.readMessage()
	<-conn.readStarted
	client.KickOnlineMessage()
	if len(ws.unregisterChan) != 0 {
		t.Fatal("kick recycled connection before its reader exited")
	}
	close(conn.readRelease)
	select {
	case got := <-ws.unregisterChan:
		if got != client {
			t.Fatal("wrong connection returned")
		}
	case <-time.After(time.Second):
		t.Fatal("reader did not return the connection")
	}
}
