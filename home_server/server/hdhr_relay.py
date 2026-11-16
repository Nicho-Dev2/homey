#!/usr/bin/python3
"""Relay between this Mac and the HDHomeRun.

Why this exists: macOS only lets a program talk to devices on the home network
if it holds the "Local Network" permission, and it does not grant that reliably
to a Python left running in the background. System services running as root are
exempt. So this small relay runs as a system service and the upscaler talks to
it on 127.0.0.1, which never needs the permission.

    127.0.0.1:8091  ->  HDHomeRun port 80    (channel list, device info)
    127.0.0.1:8092  ->  HDHomeRun port 5004  (the channel streams)

It only listens on this Mac, only connects to the one HDHomeRun, and passes
the bytes through unchanged. Install steps are in SETUP.md.

    hdhr_relay.py <HDHomeRun name or IP>
"""

import asyncio
import sys

ROUTES = {8091: 80, 8092: 5004}      # port on this Mac -> port on the HDHomeRun
CHUNK = 64 * 1024


async def copy(reader, writer):
    """Pass bytes one way until that side closes."""
    while True:
        data = await reader.read(CHUNK)
        if not data:
            return
        writer.write(data)
        await writer.drain()


async def relay(client_reader, client_writer, host, port):
    """One connection: join the caller to the HDHomeRun until either side hangs up."""
    try:
        tuner_reader, tuner_writer = await asyncio.wait_for(asyncio.open_connection(host, port), timeout=10)
    except (OSError, asyncio.TimeoutError) as e:
        print(f"cannot reach {host}:{port}: {e}", flush=True)
        client_writer.close()
        return
    both_ways = [asyncio.ensure_future(copy(client_reader, tuner_writer)),
                 asyncio.ensure_future(copy(tuner_reader, client_writer))]
    # When one side hangs up, close the other too. That is what frees the tuner
    # the moment a viewer changes channel.
    await asyncio.wait(both_ways, return_when=asyncio.FIRST_COMPLETED)
    for task in both_ways:
        task.cancel()
    for writer in (tuner_writer, client_writer):
        writer.close()


async def serve(host, routes):
    servers = []
    for listen_port, tuner_port in routes.items():
        async def handle(reader, writer, tuner_port=tuner_port):
            await relay(reader, writer, host, tuner_port)
        servers.append(await asyncio.start_server(handle, "127.0.0.1", listen_port))
        print(f"relaying 127.0.0.1:{listen_port} -> {host}:{tuner_port}", flush=True)
    await asyncio.gather(*(server.serve_forever() for server in servers))


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit(__doc__)
    asyncio.run(serve(sys.argv[1], ROUTES))
