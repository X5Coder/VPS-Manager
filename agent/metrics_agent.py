#!/usr/bin/env python3
"""VPS Rooms metrics agent — host + optional NVIDIA GPU stats over a Unix socket."""

from __future__ import annotations

import json
import os
import socket
import subprocess
import threading
import time
from http.server import BaseHTTPRequestHandler, HTTPServer
from typing import Any


SOCK = os.environ.get("VPS_ROOMS_AGENT_SOCK", "/opt/vps-rooms/data/agent.sock")
HTTP_PORT = int(os.environ.get("VPS_ROOMS_AGENT_HTTP", "0"))


def read_cpu() -> float:
    def snap():
        with open("/proc/stat") as f:
            parts = f.readline().split()
        vals = list(map(int, parts[1:9]))
        idle = vals[3] + vals[4]
        total = sum(vals)
        return idle, total

    i1, t1 = snap()
    time.sleep(0.2)
    i2, t2 = snap()
    td = t2 - t1
    idd = i2 - i1
    if td <= 0:
        return 0.0
    return round((1.0 - idd / td) * 100.0, 2)


def read_mem() -> tuple[int, int, float]:
    info: dict[str, int] = {}
    with open("/proc/meminfo") as f:
        for line in f:
            k, v, *_ = line.split()
            info[k.rstrip(":")] = int(v) * 1024
    total = info.get("MemTotal", 0)
    avail = info.get("MemAvailable", 0)
    used = max(total - avail, 0)
    pct = (used / total * 100.0) if total else 0.0
    return total, used, round(pct, 2)


def read_disk(path: str = "/") -> tuple[int, int, int, float]:
    st = os.statvfs(path)
    fr = st.f_frsize or st.f_bsize
    total = st.f_blocks * fr
    free = st.f_bavail * fr
    used = (st.f_blocks - st.f_bfree) * fr
    den = used + free
    pct = (used / den * 100.0) if den else 0.0
    return total, used, free, round(pct, 2)


def read_net() -> tuple[int, int]:
    rx = tx = 0
    with open("/proc/net/dev") as f:
        for line in f:
            if ":" not in line:
                continue
            name, rest = line.split(":", 1)
            name = name.strip()
            if name == "lo":
                continue
            fields = rest.split()
            rx += int(fields[0])
            tx += int(fields[8])
    return rx, tx


def read_load() -> float:
    with open("/proc/loadavg") as f:
        return float(f.read().split()[0])


def read_gpu() -> list[dict[str, Any]]:
    try:
        out = subprocess.check_output(
            [
                "nvidia-smi",
                "--query-gpu=name,utilization.gpu,memory.used,memory.total",
                "--format=csv,noheader,nounits",
            ],
            stderr=subprocess.DEVNULL,
            text=True,
            timeout=2,
        )
    except Exception:
        return []
    gpus = []
    for line in out.strip().splitlines():
        parts = [p.strip() for p in line.split(",")]
        if len(parts) < 4:
            continue
        gpus.append(
            {
                "name": parts[0],
                "util_percent": float(parts[1] or 0),
                "mem_used_mb": float(parts[2] or 0),
                "mem_total_mb": float(parts[3] or 0),
            }
        )
    return gpus


def collect() -> dict[str, Any]:
    mem_t, mem_u, mem_p = read_mem()
    disk_t, disk_u, disk_f, disk_p = read_disk("/")
    rx, tx = read_net()
    return {
        "cpu_percent": read_cpu(),
        "cpu_cores": os.cpu_count() or 1,
        "mem_total": mem_t,
        "mem_used": mem_u,
        "mem_percent": mem_p,
        "disk_total": disk_t,
        "disk_used": disk_u,
        "disk_free": disk_f,
        "disk_percent": disk_p,
        "net_rx": rx,
        "net_tx": tx,
        "load1": read_load(),
        "gpu": read_gpu(),
        "timestamp": int(time.time()),
    }


def serve_unix():
    if os.path.exists(SOCK):
        os.remove(SOCK)
    os.makedirs(os.path.dirname(SOCK), exist_ok=True)
    srv = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    srv.bind(SOCK)
    os.chmod(SOCK, 0o660)
    srv.listen(16)
    print(f"agent listening on {SOCK}", flush=True)
    while True:
        conn, _ = srv.accept()
        try:
            conn.recv(64)
            data = json.dumps(collect()).encode()
            conn.sendall(data + b"\n")
        except Exception:
            pass
        finally:
            conn.close()


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        body = json.dumps(collect()).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_args):
        return


def main():
    t = threading.Thread(target=serve_unix, daemon=True)
    t.start()
    if HTTP_PORT > 0:
        HTTPServer(("127.0.0.1", HTTP_PORT), Handler).serve_forever()
    else:
        while True:
            time.sleep(3600)


if __name__ == "__main__":
    main()
