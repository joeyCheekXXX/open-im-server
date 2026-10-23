package glbplatform_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type auth struct {
	Token   string `json:"token"`
	IMToken string `json:"imToken"`
	UserID  string `json:"userId"`
	WSAddr  string `json:"wsAddr"`
}
type response struct {
	Code int             `json:"code"`
	Data json.RawMessage `json:"data"`
}
type connection struct {
	socket *websocket.Conn
	done   chan struct{}
	pong   chan struct{}
}

// This probe is opt-in and intentionally replaces sessions for two dedicated
// accounts. It never creates accounts, sends messages, or accesses real groups.
func TestGLBPlatformRealServices(t *testing.T) {
	base := os.Getenv("GLB_PROBE_BASE_URL")
	if base == "" {
		t.Skip("real-service probe not run: GLB_PROBE_BASE_URL is unset")
	}
	if os.Getenv("GLB_PROBE_DEDICATED_ACCOUNTS") != "yes" {
		t.Fatal("set GLB_PROBE_DEDICATED_ACCOUNTS=yes only for two dedicated test accounts")
	}
	accountA, accountB := os.Getenv("GLB_PROBE_ACCOUNT_A"), os.Getenv("GLB_PROBE_ACCOUNT_B")
	passwordA, passwordB := os.Getenv("GLB_PROBE_PASSWORD_A"), os.Getenv("GLB_PROBE_PASSWORD_B")
	if accountA == "" || accountB == "" || accountA == accountB || passwordA == "" || passwordB == "" {
		t.Fatal("two distinct test accounts and passwords are required")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	nonce := strconv.FormatInt(time.Now().UnixNano(), 10)
	call := func(path string, platform int, clientID, token string, body any) (int, response) {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(http.MethodPost, strings.TrimRight(base, "/")+"/api/v1/"+path, bytes.NewReader(data))
		if err != nil {
			t.Fatal("invalid business API address")
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Client-ID", clientID)
		req.Header.Set("X-Platform", strconv.Itoa(platform))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		result, err := client.Do(req)
		if err != nil {
			t.Fatal("business API unreachable")
		}
		defer result.Body.Close()
		var payload response
		if err := json.NewDecoder(result.Body).Decode(&payload); err != nil {
			t.Fatal("invalid business API JSON")
		}
		return result.StatusCode, payload
	}
	login := func(account, password string, platform int, device string) auth {
		id := "glb-probe-" + nonce + "-" + device
		status, result := call("auth/login", platform, id, "", map[string]string{"publicAccountId": account, "password": password, "deviceName": id})
		if status != 200 || result.Code != 0 {
			t.Fatalf("login platform %d failed: HTTP %d / code %d", platform, status, result.Code)
		}
		var out auth
		if err := json.Unmarshal(result.Data, &out); err != nil || out.Token == "" || out.IMToken == "" {
			t.Fatal("login omitted credentials")
		}
		t.Cleanup(func() { call("account/logout", platform, id, out.Token, map[string]any{}) })
		return out
	}
	dial := func(a auth, platform int) (*websocket.Conn, int) {
		u, err := url.Parse(a.WSAddr)
		if err != nil {
			t.Fatal("invalid returned WS address")
		}
		q := u.Query()
		q.Set("sendID", a.UserID)
		q.Set("token", a.IMToken)
		q.Set("platformID", strconv.Itoa(platform))
		q.Set("operationID", "glb-probe-"+nonce)
		q.Set("isBackground", "false")
		q.Set("isMsgResp", "true")
		u.RawQuery = q.Encode()
		d := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
		socket, res, err := d.Dial(u.String(), nil)
		if err != nil {
			if res != nil {
				res.Body.Close()
				return nil, res.StatusCode
			}
			return nil, 0
		}
		return socket, 101
	}
	connect := func(a auth, platform int) *connection {
		socket, status := dial(a, platform)
		if socket == nil {
			t.Fatalf("platform %d WS handshake rejected: HTTP %d", platform, status)
		}
		t.Cleanup(func() { socket.Close() })
		socket.SetReadDeadline(time.Now().Add(10 * time.Second))
		_, data, err := socket.ReadMessage()
		if err != nil {
			t.Fatal("gateway did not confirm connection")
		}
		var ready struct {
			ErrCode int `json:"errCode"`
		}
		if err := json.Unmarshal(data, &ready); err != nil || ready.ErrCode != 0 {
			t.Fatal("gateway rejected connection registration")
		}
		socket.SetReadDeadline(time.Time{})
		c := &connection{socket: socket, done: make(chan struct{}), pong: make(chan struct{}, 1)}
		socket.SetPongHandler(func(string) error {
			select {
			case c.pong <- struct{}{}:
			default:
			}
			return nil
		})
		go func() {
			defer close(c.done)
			for {
				if _, _, err := socket.ReadMessage(); err != nil {
					return
				}
			}
		}()
		return c
	}
	alive := func(c *connection) {
		if err := c.socket.WriteControl(websocket.PingMessage, []byte("glb"), time.Now().Add(5*time.Second)); err != nil {
			t.Fatal("coexisting WS closed")
		}
		select {
		case <-c.pong:
		case <-c.done:
			t.Fatal("coexisting WS disconnected")
		case <-time.After(5 * time.Second):
			t.Fatal("coexisting WS did not pong")
		}
	}
	var sessions []auth
	var connections []*connection
	for _, platform := range []int{1, 2, 5} {
		a := login(accountA, passwordA, platform, fmt.Sprintf("platform-%d", platform))
		sessions = append(sessions, a)
		connections = append(connections, connect(a, platform))
	}
	old := login(accountA, passwordA, 13, "glb-old")
	oldConn := connect(old, 13)
	other := login(accountB, passwordB, 13, "glb-other")
	connections = append(connections, connect(other, 13))
	sessions = append(sessions, other)
	fresh := login(accountA, passwordA, 13, "glb-new")
	connections = append(connections, connect(fresh, 13))
	sessions = append(sessions, fresh)
	status, _ := call("account/current", 13, "old", old.Token, map[string]any{})
	if status != 401 {
		t.Fatal("old GLB business credential remains accepted")
	}
	select {
	case <-oldConn.done:
	case <-time.After(15 * time.Second):
		t.Fatal("old GLB IM connection was not closed")
	}
	socket, status := dial(old, 13)
	if socket == nil {
		t.Fatalf("old credential rejection was not observable: HTTP %d", status)
	}
	socket.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, rejected, err := socket.ReadMessage()
	socket.Close()
	var rejection struct {
		ErrCode int `json:"errCode"`
	}
	if err != nil || json.Unmarshal(rejected, &rejection) != nil || rejection.ErrCode != 1506 {
		t.Fatal("old GLB IM credential did not return the expected kicked-token error")
	}
	for i, a := range sessions {
		status, r := call("account/current", 13, "check", a.Token, map[string]any{})
		if status != 200 || r.Code != 0 {
			t.Fatalf("coexisting business session %d invalid", i)
		}
		alive(connections[i])
	}
	status, r := call("account/sessions", 13, "check", fresh.Token, map[string]any{})
	var listing struct {
		List []struct {
			Platform string `json:"platform"`
		} `json:"list"`
	}
	if status != 200 || r.Code != 0 || json.Unmarshal(r.Data, &listing) != nil {
		t.Fatal("session listing failed")
	}
	platforms := map[string]int{}
	for _, s := range listing.List {
		platforms[s.Platform]++
	}
	if len(platforms) != 4 || platforms["glb"] != 1 || platforms["ios"] != 1 || platforms["android"] != 1 || platforms["web"] != 1 {
		t.Fatalf("unexpected platform counts: %v", platforms)
	}
	t.Log("real HTTP + WebSocket coexistence, GLB replacement, other-account isolation, old credential rejection and session listing passed")
}
