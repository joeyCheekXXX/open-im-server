#!/usr/bin/env python3
"""用临时 Nginx 容器和模拟上游验收网关策略，不接触真实 OpenIM 数据。"""

import argparse
import base64
import hashlib
import http.client
import json
from pathlib import Path
import socket
import struct
import subprocess
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


def read_exact(stream, length):
    data = b""
    while len(data) < length:
        chunk = stream.read(length - len(data))
        if not chunk:
            raise EOFError("WebSocket frame incomplete")
        data += chunk
    return data


class Upstream(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *_):
        pass

    def do_POST(self):
        self.do_GET()

    def do_GET(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        self.server.requests.append((self.command, self.path))
        if self.headers.get("Upgrade", "").lower() == "websocket":
            key = self.headers["Sec-WebSocket-Key"]
            accept = base64.b64encode(hashlib.sha1(
                (key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode()
            ).digest()).decode()
            self.send_response(101)
            self.send_header("Upgrade", "websocket")
            self.send_header("Connection", "Upgrade")
            self.send_header("Sec-WebSocket-Accept", accept)
            self.end_headers()
            self.wfile.flush()
            first, second = read_exact(self.rfile, 2)
            assert first == 0x82 and second & 0x80
            length = second & 0x7F
            assert length < 126
            mask = read_exact(self.rfile, 4)
            masked = read_exact(self.rfile, length)
            payload = bytes(value ^ mask[i % 4] for i, value in enumerate(masked))
            self.wfile.write(bytes([0x82, length]) + payload)
            self.wfile.flush()
            self.close_connection = True
            return
        response = json.dumps({
            "path": self.path,
            "body": body.decode(),
            "token": self.headers.get("token"),
            "operationID": self.headers.get("operationID"),
            "apiURL": self.headers.get("X-Request-Api"),
        }).encode()
        self.send_response(200)
        # 确认网关覆盖上游 CORS，不产生重复响应头。
        self.send_header("Access-Control-Allow-Origin", "https://upstream.invalid")
        self.send_header("Content-Length", str(len(response)))
        self.end_headers()
        self.wfile.write(response)


def docker(*args):
    return subprocess.check_output(["docker", *args], text=True, stderr=subprocess.STDOUT).strip()


def request(port, method, path, body=None):
    connection = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
    try:
        connection.request(method, path, body, {
            "Content-Type": "application/json", "token": "test-api-token",
            "operationID": "test-operation", "Origin": "https://client.invalid",
        })
        response = connection.getresponse()
        return response.status, response.getheaders(), response.read()
    finally:
        connection.close()


def check(condition, message):
    if not condition:
        raise AssertionError(message)


def run_case(image, public_api_url):
    root = Path(__file__).resolve().parent
    backend = ThreadingHTTPServer(("0.0.0.0", 0), Upstream)
    backend.daemon_threads = True
    backend.requests = []
    thread = threading.Thread(target=backend.serve_forever, daemon=True)
    thread.start()
    container = None
    try:
        upstream = "host.docker.internal:" + str(backend.server_port)
        container = docker(
            "run", "--rm", "-d", "--add-host", "host.docker.internal:host-gateway",
            "-p", "127.0.0.1::8080", "-p", "127.0.0.1::8081",
            "-e", "OPENIM_API_UPSTREAM=" + upstream,
            "-e", "OPENIM_WS_UPSTREAM=" + upstream,
            "-e", "OPENIM_PUBLIC_API_URL=" + public_api_url,
            "-e", "NGINX_ENVSUBST_FILTER=^(OPENIM_API_UPSTREAM|OPENIM_WS_UPSTREAM|OPENIM_PUBLIC_API_URL)$",
            "-v", str(root / "templates") + ":/etc/nginx/templates:ro",
            "-v", str(root / "client-api-allowlist.map") + ":/etc/nginx/openim/client-api-allowlist.map:ro",
            image,
        )
        ports = json.loads(docker("inspect", "--format", "{{json .NetworkSettings.Ports}}", container))
        api = int(ports["8080/tcp"][0]["HostPort"])
        ws = int(ports["8081/tcp"][0]["HostPort"])
        deadline = time.monotonic() + 10
        while True:
            try:
                check(request(api, "GET", "/healthz")[0] == 200, "Nginx health")
                break
            except (OSError, http.client.HTTPException):
                if time.monotonic() >= deadline:
                    raise
                time.sleep(0.1)
        docker("exec", container, "nginx", "-t")

        blocked = [
            "/friend/delete_friend", "/friend/add_friend", "/friend/update_friends",
            "/friend/is_friend", "/group/create_group", "/group/kick_group",
            "/user/update_user_info_ex", "/auth/get_admin_token", "/auth/get_user_token",
            "/msg/send_msg", "/msg/revoke_msg", "/unknown/new_api",
            "/friend/get_friend_list/../delete_friend", "/friend/%64elete_friend",
            "//friend/delete_friend", "/friend/get_friend_list%2f..%2fdelete_friend",
            "/object/../friend/delete_friend", "/friend/get_friend_list/extra",
        ]
        before = len(backend.requests)
        for path in blocked:
            check(request(api, "POST", path, "{}")[0] == 403, "must deny " + path)
        check(request(api, "OPTIONS", "/friend/delete_friend")[0] == 403, "blocked preflight")
        check(request(api, "GET", "/third/prometheus")[0] == 403, "monitoring denied")
        check(request(api, "GET", "/friend/get_friend_list")[0] == 405, "REST GET denied")
        check(request(api, "PUT", "/friend/get_friend_list", "{}")[0] == 405, "REST PUT denied")
        check(len(backend.requests) == before, "denied requests reached upstream")

        allowed = [
            "/auth/parse_token", "/friend/get_incremental_friends",
            "/group/get_incremental_group_members_batch", "/conversation/set_conversations",
            "/msg/mark_conversation_as_read", "/msg/user_clear_all_msg", "/object/access_url",
        ]
        for path in allowed:
            status, headers, data = request(api, "POST", path + "?page=2", '{"owner":"test"}')
            check(status == 200, "must proxy " + path)
            forwarded = json.loads(data)
            check(forwarded["path"] == path + "?page=2", "query preservation")
            check(forwarded["token"] == "test-api-token", "token preservation")
            check(forwarded["operationID"] == "test-operation", "operationID preservation")
            check(forwarded["body"] == '{"owner":"test"}', "body preservation")
            expected_url = public_api_url or "http://127.0.0.1:" + str(api)
            check(forwarded["apiURL"] == expected_url, "object URL origin")
            check([v for k, v in headers if k.lower() == "access-control-allow-origin"] == ["*"], "CORS duplication")
        check(request(api, "OPTIONS", "/friend/get_friend_list")[0] == 204, "allowed preflight")
        status, _, data = request(api, "GET", "/object/path/file.jpg?size=original")
        check(status == 200 and json.loads(data)["path"].endswith("?size=original"), "object download")
        status, _, data = request(api, "POST", "/friend/%67et_friend_list", "{}")
        check(status == 200 and json.loads(data)["path"] == "/friend/get_friend_list", "canonical upstream path")

        check(request(ws, "GET", "/admin")[0] == 403, "WS path restriction")
        check(request(ws, "GET", "/")[0] == 426, "WS upgrade required")
        check(request(ws, "POST", "/", "{}")[0] == 405, "WS method restriction")
        with socket.create_connection(("127.0.0.1", ws), timeout=5) as connection:
            stream = connection.makefile("rb")
            connection.sendall((
                "GET /?token=test-ws-secret&sendID=test HTTP/1.1\r\n"
                "Host: localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
                "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n"
            ).encode())
            check(b" 101 " in stream.readline(), "WS upgrade response")
            while True:
                line = stream.readline()
                check(bool(line), "WS handshake headers incomplete")
                if line == b"\r\n":
                    break
            payload = b"binary-message"
            mask = b"abcd"
            masked = bytes(value ^ mask[i % 4] for i, value in enumerate(payload))
            connection.sendall(struct.pack("BB", 0x82, 0x80 | len(payload)) + mask + masked)
            check(read_exact(stream, 2) == bytes([0x82, len(payload)]), "WS binary response header")
            check(read_exact(stream, len(payload)) == payload, "WS binary data")
            stream.close()
        check(("GET", "/?token=test-ws-secret&sendID=test") in backend.requests, "WS query preservation")
        # 等待请求关闭后写入访问日志。
        time.sleep(0.1)
        logs = docker("logs", container)
        check("test-ws-secret" not in logs and "test-api-token" not in logs, "tokens leaked to log")
        print("PASS: API deny/allow, methods, CORS, normalized paths, headers/body, object URL, WS handshake/binary/logs; public URL=" + (public_api_url or "request origin"))
    except Exception:
        if container:
            print(docker("logs", container))
        raise
    finally:
        if container:
            docker("rm", "-f", container)
        backend.shutdown()
        backend.server_close()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", default="nginx:1.30.5-alpine")
    arguments = parser.parse_args()
    run_case(arguments.image, "")
    run_case(arguments.image, "https://im-api.example.invalid")
