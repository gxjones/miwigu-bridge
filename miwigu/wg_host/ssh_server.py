#!/usr/bin/env python3
"""SSH server on port 22. Every session writes a success message and exits."""

import asyncio
import asyncssh

HOST = "0.0.0.0"
PORT = 22


class Server(asyncssh.SSHServer):
    def begin_auth(self, username):
        return False

    def connection_made(self, conn):
        peer = conn.get_extra_info("peername")
        where = f"{peer[0]}:{peer[1]}" if peer else "?"
        print(f"[port22] {where} connected", flush=True)

    def connection_lost(self, exc):
        if exc:
            print(f"[port22] connection error {exc}", flush=True)


async def handle_client(process):
    command = process.command or "shell"
    print(f"[port22] session {command}", flush=True)
    process.stdout.write("success\n")
    process.exit(0)


async def main():
    host_key = asyncssh.generate_private_key("ssh-ed25519")
    await asyncssh.create_server(
        Server,
        HOST,
        PORT,
        server_host_keys=[host_key],
        process_factory=handle_client,
    )
    print(f"ssh server listening on {HOST}:{PORT}", flush=True)
    await asyncio.Future()


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except (OSError, asyncssh.Error) as exc:
        raise SystemExit(f"ssh server failed: {exc}") from exc
