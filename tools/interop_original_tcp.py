#!/usr/bin/env python3
"""Run localhost TCP interoperability checks against the retained original binary."""

from __future__ import annotations

import argparse
import socket
import subprocess
import tempfile
import threading
import time
from pathlib import Path


TOKEN = "secret"


def free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


class EchoServer:
    def __init__(self) -> None:
        self.listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        self.listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        self.listener.bind(("127.0.0.1", 0))
        self.listener.listen()
        self.listener.settimeout(0.2)
        self.port = int(self.listener.getsockname()[1])
        self.stopped = threading.Event()
        self.thread = threading.Thread(target=self._serve, daemon=True)

    def start(self) -> None:
        self.thread.start()

    def close(self) -> None:
        self.stopped.set()
        self.listener.close()
        self.thread.join(timeout=1)

    def _serve(self) -> None:
        while not self.stopped.is_set():
            try:
                conn, _ = self.listener.accept()
            except (TimeoutError, socket.timeout):
                continue
            except OSError:
                return
            threading.Thread(target=self._echo, args=(conn,), daemon=True).start()

    @staticmethod
    def _echo(conn: socket.socket) -> None:
        with conn:
            while True:
                data = conn.recv(65536)
                if not data:
                    return
                conn.sendall(data)


def server_config(control: int, public: int, target: int) -> str:
    return f'''[listener]
bind_addr = "127.0.0.1:{control}"

[transport]
type = "tcp"
nodelay = true
keepalive_period = 40
accept_udp = false
proxy_protocol = false
heartbeat_interval = 1
heartbeat_timeout = 5

[security]
token = "{TOKEN}"

[tuning]
auto_tuning = false
tuning_profile = "balanced"
workers = 1
channel_size = 64
tcp_mss = 0
so_rcvbuf = 0
so_sndbuf = 0
buffer_profile = "balanced"
read_timeout = 30

[logging]
log_level = "debug"

[ports]
mapping = ["{public}={target}"]
'''


def client_config(control: int) -> str:
    return f'''[dialer]
remote_addr = "127.0.0.1:{control}"
dial_timeout = 2
retry_interval = 1

[transport]
type = "tcp"
connection_pool = 2
nodelay = true
keepalive_period = 40
heartbeat_interval = 1
heartbeat_timeout = 5

[security]
token = "{TOKEN}"

[tuning]
auto_tuning = false
tuning_profile = "balanced"
workers = 1
channel_size = 64
tcp_mss = 0
so_rcvbuf = 0
so_sndbuf = 0
buffer_profile = "balanced"
read_timeout = 30

[logging]
log_level = "debug"
'''


def start(binary: Path, config: Path) -> subprocess.Popen[str]:
    return subprocess.Popen(
        [str(binary), "-c", str(config)],
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
    )


def stop(process: subprocess.Popen[str]) -> str:
    if process.poll() is None:
        process.terminate()
    try:
        output, _ = process.communicate(timeout=3)
    except subprocess.TimeoutExpired:
        process.kill()
        output, _ = process.communicate(timeout=2)
    return output


def round_trip(port: int, payload: bytes, timeout: float = 12.0) -> None:
    deadline = time.monotonic() + timeout
    last_error: Exception | None = None
    while time.monotonic() < deadline:
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=0.5) as conn:
                conn.settimeout(1)
                conn.sendall(payload)
                received = bytearray()
                while len(received) < len(payload):
                    chunk = conn.recv(len(payload) - len(received))
                    if not chunk:
                        raise ConnectionError("relay closed before echo completed")
                    received.extend(chunk)
                if bytes(received) != payload:
                    raise AssertionError(f"echo mismatch: {received!r}")
                return
        except (OSError, AssertionError) as exc:
            last_error = exc
            time.sleep(0.1)
    raise TimeoutError(f"relay did not pass traffic: {last_error}")


def run_scenario(
    label: str,
    original: Path,
    recovered: Path,
    original_is_server: bool,
    stability_seconds: float,
) -> tuple[str, str]:
    echo = EchoServer()
    echo.start()
    control = free_port()
    public = free_port()
    with tempfile.TemporaryDirectory(prefix="backhaul-interop-") as temp:
        temp_path = Path(temp)
        server_path = temp_path / "server.toml"
        client_path = temp_path / "client.toml"
        server_path.write_text(server_config(control, public, echo.port), encoding="utf-8")
        client_path.write_text(client_config(control), encoding="utf-8")

        server_binary = original if original_is_server else recovered
        client_binary = recovered if original_is_server else original
        server = start(server_binary, server_path)
        time.sleep(0.25)
        client = start(client_binary, client_path)
        server_log = ""
        client_log = ""
        trip_error: Exception | None = None
        try:
            round_trip(public, label.encode("ascii"))
            if stability_seconds > 0:
                time.sleep(stability_seconds)
                round_trip(public, (label + "-stable").encode("ascii"))
        except Exception as exc:
            trip_error = exc
        finally:
            client_log = stop(client)
            server_log = stop(server)
            echo.close()
        if trip_error is not None:
            raise RuntimeError(
                f"{label}: {trip_error}\n--- server ---\n{server_log}"
                f"\n--- client ---\n{client_log}"
            ) from trip_error
        if server.returncode not in (0, -15) and "shutdown signal received" not in server_log:
            raise RuntimeError(f"{label}: server exited {server.returncode}\n{server_log}")
        return server_log, client_log


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--original", required=True, type=Path)
    parser.add_argument("--recovered", default=Path("dist/backhaul_recovered"), type=Path)
    parser.add_argument(
        "--scenario",
        choices=("both", "original-server", "recovered-server"),
        default="both",
    )
    parser.add_argument("--stability-seconds", type=float, default=0)
    args = parser.parse_args()
    original = args.original.resolve()
    recovered = args.recovered.resolve()
    if not original.is_file() or not recovered.is_file():
        parser.error("both --original and --recovered must point to binaries")

    scenarios = (
        ("original-server_recovered-client", True),
        ("recovered-server_original-client", False),
    )
    if args.scenario == "original-server":
        scenarios = scenarios[:1]
    elif args.scenario == "recovered-server":
        scenarios = scenarios[1:]

    for label, original_is_server in scenarios:
        server_log, client_log = run_scenario(
            label,
            original,
            recovered,
            original_is_server,
            args.stability_seconds,
        )
        print(f"PASS {label}")
        print(f"  server_log_lines={len(server_log.splitlines())}")
        print(f"  client_log_lines={len(client_log.splitlines())}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
