#!/usr/bin/env python3
"""Minimal RFC6455 WebSocket client for the scnetm instance console.

Deliberately dependency-free (no websocket-client / websockets package needed):
it speaks just enough of RFC6455 to open the console socket, send text frames
and decode server frames.  Used by the V0 probe scripts.

Usage:
  ws_console.py --url ws://host/ws/instances/1/console [--token T]
                [--send "help"] [--seconds 20] [--raw]
                [--repeat N --interval S]

Prints one line per received frame:
  <type> <repr-or-text>
where <type> is one of: text, binary, ping, pong, close, error, note, sent.
With --raw it prints the frame payload bytes verbatim (escaped) instead.
"""

import argparse
import base64
import hashlib
import json
import os
import socket
import ssl
import struct
import sys
import time
from urllib.parse import urlparse, urlencode, quote


def recv_exact(sock, n):
    buf = b""
    while len(buf) < n:
        chunk = sock.recv(n - len(buf))
        if not chunk:
            raise ConnectionError("socket closed")
        buf += chunk
    return buf


def read_frame(sock):
    """Return (opcode, payload_bytes, fin)."""
    b1, b2 = recv_exact(sock, 2)
    fin = bool(b1 & 0x80)
    opcode = b1 & 0x0F
    masked = bool(b2 & 0x80)
    length = b2 & 0x7F
    if length == 126:
        length = struct.unpack(">H", recv_exact(sock, 2))[0]
    elif length == 127:
        length = struct.unpack(">Q", recv_exact(sock, 8))[0]
    mask = recv_exact(sock, 4) if masked else None
    payload = recv_exact(sock, length) if length else b""
    if mask:
        payload = bytes(c ^ mask[i % 4] for i, c in enumerate(payload))
    return opcode, payload, fin


def send_text(sock, text):
    data = text.encode("utf-8")
    header = bytearray()
    header.append(0x81)  # FIN + text
    n = len(data)
    if n < 126:
        header.append(0x80 | n)
    elif n < 65536:
        header.append(0x80 | 126)
        header += struct.pack(">H", n)
    else:
        header.append(0x80 | 127)
        header += struct.pack(">Q", n)
    mask = os.urandom(4)
    header += mask
    masked = bytes(c ^ mask[i % 4] for i, c in enumerate(data))
    sock.sendall(bytes(header) + masked)


def send_close(sock, code=1000):
    payload = struct.pack(">H", code)
    mask = os.urandom(4)
    frame = bytearray([0x88, 0x80 | len(payload)])
    frame += mask
    frame += bytes(c ^ mask[i % 4] for i, c in enumerate(payload))
    try:
        sock.sendall(bytes(frame))
    except OSError:
        pass


def connect(url, token=None, timeout=10):
    u = urlparse(url)
    host = u.hostname
    port = u.port or (443 if u.scheme == "wss" else 80)
    path = u.path or "/"
    if u.query:
        path += "?" + u.query
    if token:
        path += ("&" if "?" in path else "?") + "token=" + quote(token)

    sock = socket.create_connection((host, port), timeout=timeout)
    if u.scheme == "wss":
        ctx = ssl.create_default_context()
        sock = ctx.wrap_socket(sock, server_hostname=host)

    key = base64.b64encode(os.urandom(16)).decode()
    req = (
        f"GET {path} HTTP/1.1\r\n"
        f"Host: {host}:{port}\r\n"
        "Upgrade: websocket\r\n"
        "Connection: Upgrade\r\n"
        f"Sec-WebSocket-Key: {key}\r\n"
        "Sec-WebSocket-Version: 13\r\n"
        "Origin: http://%s:%d\r\n" % (host, port)
        + "\r\n"
    )
    sock.sendall(req.encode())

    # Read the HTTP handshake response headers.
    raw = b""
    while b"\r\n\r\n" not in raw:
        chunk = sock.recv(4096)
        if not chunk:
            raise ConnectionError("handshake closed early")
        raw += chunk
    head, rest = raw.split(b"\r\n\r\n", 1)
    status = head.split(b"\r\n", 1)[0].decode(errors="replace")
    if b"101" not in head.split(b"\r\n", 1)[0]:
        raise ConnectionError(f"handshake failed: {status}")
    return sock, rest, status


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--url", required=True)
    ap.add_argument("--token", default=None)
    ap.add_argument("--send", default=None, help="line to send once connected")
    ap.add_argument("--seconds", type=float, default=20.0)
    ap.add_argument("--repeat", type=int, default=1)
    ap.add_argument("--interval", type=float, default=1.0)
    ap.add_argument("--raw", action="store_true")
    ap.add_argument("--json-send", default=None,
                    help="send this exact JSON string (no newline)")
    args = ap.parse_args()

    try:
        sock, leftover, status = connect(args.url, args.token)
    except Exception as e:  # noqa: BLE001
        print(f"error connect: {e}")
        return 2
    print(f"note handshake {status}")

    sock.settimeout(0.5)
    deadline = time.time() + args.seconds
    sent = 0
    sends = []
    if args.json_send is not None:
        sends.append(args.json_send)
    elif args.send is not None:
        for _ in range(args.repeat):
            sends.append(args.send)
    next_send = time.time() + 1.0

    buf = leftover
    while time.time() < deadline:
        if sent < len(sends) and time.time() >= next_send:
            try:
                send_text(sock, sends[sent])
                print(f"sent {sends[sent]!r}")
            except OSError as e:
                print(f"error send: {e}")
                break
            sent += 1
            next_send = time.time() + args.interval
        try:
            op, payload, _fin = read_frame(sock)
        except socket.timeout:
            continue
        except Exception as e:  # noqa: BLE001
            print(f"error recv: {e}")
            break
        if op == 0x1:
            print(f"text {payload.decode('utf-8', 'replace')!r}")
        elif op == 0x2:
            if args.raw:
                print(f"binary {payload!r}")
            else:
                print(f"binary {payload.decode('utf-8','replace')!r}")
        elif op == 0x8:
            print(f"close {payload!r}")
            break
        elif op == 0x9:
            print(f"ping {payload!r}")
        elif op == 0xA:
            print(f"pong {payload!r}")
        else:
            print(f"op{op} {payload!r}")

    send_close(sock)
    sock.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
