#!/usr/bin/env python3
"""Isolated Nicotine+ profile peer for mandatory local interoperability tests.

Uses the unmodified reference application's network and user-info components,
not an oto encoder or a replacement peer implementation. No GUI or public server.
"""
import json
import os
from pathlib import Path
import subprocess
import sys
import time

sys.dont_write_bytecode = True
reference, state, server, listen_port = sys.argv[1:]
reference, state = Path(reference).resolve(), Path(state).resolve()
host, port = server.rsplit(":", 1)
if host != "127.0.0.1" or not 0 < int(port) < 65536 or not 0 < int(listen_port) < 65536:
    raise SystemExit("test peer requires explicit loopback ports")
corpus = json.loads((Path(__file__).resolve().parents[1] / "internal/testutil/testdata/social.json").read_text())
revision = subprocess.check_output(["git", "-C", str(reference), "rev-parse", "HEAD"], text=True).strip()
if revision != corpus["nicotine_revision"]:
    raise SystemExit("Nicotine+ revision mismatch")
if subprocess.check_output(["git", "-C", str(reference), "status", "--porcelain", "--untracked-files=no"]):
    raise SystemExit("reference has local modifications")
os.environ.update(HOME=str(state), XDG_CONFIG_HOME=str(state / "config"),
                  XDG_DATA_HOME=str(state / "data"), XDG_CACHE_HOME=str(state / "cache"))
sys.path.insert(0, str(reference))
from pynicotine.i18n import apply_translations  # noqa: E402
apply_translations()
from pynicotine.config import config  # noqa: E402
from pynicotine.core import core  # noqa: E402
from pynicotine.events import events  # noqa: E402


def publish(name, value):
    temporary = state / (name + ".tmp")
    temporary.write_text(json.dumps(value, ensure_ascii=False))
    temporary.replace(state / name)


def logged_in(message):
    if not message.success:
        raise RuntimeError("local reference login failed")
    publish("ready.json", {"revision": revision, "username": "reference"})


def received_profile(message):
    if message.username == "terminal":
        publish("response.json", {"username": message.username, "description": message.descr,
                                 "slots": message.totalupl, "queue": message.queuesize,
                                 "available": message.slotsavail, "upload_allowed": message.uploadallowed})


if __name__ == "__main__":
    # Nicotine's share scanner uses multiprocessing.spawn, which imports this file.
    core.init_components({"signal_handler", "network_thread", "users", "shares", "uploads", "downloads",
                          "userinfo", "network_filter", "buddies", "statistics", "pluginhandler"}, isolated_mode=True)
    config.sections["server"].update(server=(host, int(port)), login="reference", passw="local-test-only",
                                     upnp=False, auto_connect_startup=False)
    config.sections["userinfo"].update(descr=repr("Nicotine reference 世界"), pic=str(state / "picture.png"))
    config.sections["transfers"].update(shared=[], buddyshared=[], trustedshared=[], remotedownloads=False)
    core.cli_interface_address = "127.0.0.1"
    core.cli_listen_port = int(listen_port)
    events.connect("server-login", logged_in)
    events.connect("user-info-response", received_profile)
    core.start()
    core.connect()
    requested = False
    stopping = False
    deadline = time.monotonic() + 45
    while events.process_thread_events():
        if (state / "request").exists() and not requested:
            requested = True
            core.userinfo.show_user("terminal", refresh=True)
        if (state / "stop").exists() and not stopping:
            stopping = True
            core.quit()
        if time.monotonic() > deadline:
            raise RuntimeError("reference profile scenario timed out")
        time.sleep(0.02)
    if not stopping:
        raise RuntimeError("reference peer stopped before test completion")
