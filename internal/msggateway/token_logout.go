package msggateway

import (
	"context"
	"time"

	"github.com/openimsdk/protocol/msggateway"
	"github.com/openimsdk/tools/errs"
)

func (s *Server) KickConnectionByToken(ctx context.Context, req *msggateway.KickConnectionByTokenReq) (*msggateway.KickConnectionByTokenResp, error) {
	if err := req.Check(); err != nil {
		return nil, errs.ErrArgs.WrapMsg("invalid token kick request")
	}
	if err := s.LongConnServer.KickConnectionByToken(ctx, req.UserID, int(req.PlatformID), req.Token); err != nil {
		return nil, err
	}
	return &msggateway.KickConnectionByTokenResp{}, nil
}

func (ws *WsServer) KickConnectionByToken(ctx context.Context, userID string, platformID int, token string) error {
	kick := &tokenKick{userID: userID, platformID: platformID, token: token, done: make(chan error, 1)}
	select {
	case ws.kickTokenChan <- kick:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-kick.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Both connection lookup and kick execute in the registration event loop.
// Client objects cannot be returned to the pool and reused during this operation.
func (ws *WsServer) kickConnectionsByToken(kick *tokenKick) error {
	clients, _, _ := ws.clients.Get(kick.userID, kick.platformID)
	for _, client := range clients {
		if client.UserID == kick.userID && client.PlatformID == kick.platformID && client.token == kick.token && !client.closed.Load() {
			// A failed notification write still closes the socket.
			_ = client.KickOnlineMessage()
		}
	}
	return nil
}

func (ws *WsServer) registerValidatedClient(registration *clientRegistration) error {
	client := registration.client
	ctx, cancel := context.WithTimeout(client.ctx, 5*time.Second)
	defer cancel()
	resp, err := ws.authClient.ParseToken(ctx, client.token)
	if err != nil {
		return errs.ErrNoPermission.WrapMsg("registration token is no longer valid")
	}
	if err := ws.validateRespWithRequest(client.ctx, resp); err != nil {
		return err
	}
	ws.registerClient(client)
	if registration.ready != nil {
		if err := registration.ready(); err != nil {
			_ = client.conn.Close()
		}
	}
	go client.readMessage()
	return nil
}

// Delayed cross-gateway login events must not let a revoked token kick a newer
// login. Re-resolve connections here so queued events never hold pooled clients.
func (ws *WsServer) checkRemoteLogin(client *Client) {
	ctx, cancel := context.WithTimeout(client.ctx, 5*time.Second)
	defer cancel()
	resp, err := ws.authClient.ParseToken(ctx, client.token)
	if err != nil || ws.validateRespWithRequest(client.ctx, resp) != nil {
		return
	}
	oldClients, userOK, clientOK := ws.clients.Get(client.UserID, client.PlatformID)
	if userOK {
		ws.multiTerminalLoginChecker(clientOK, oldClients, client)
	}
}
