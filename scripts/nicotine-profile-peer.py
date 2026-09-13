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

def received_shares(message):
    if message.username == "terminal":
        publish(f"shares-{requested_shares}.json", {
            "public": sorted(folder for folder, files in message.list if files),
            "locked": sorted(folder for folder, files in message.privatelist if files),
        })


if __name__ == "__main__":
    # Nicotine's share scanner uses multiprocessing.spawn, which imports this file.
    core.init_components({"signal_handler", "network_thread", "users", "shares", "uploads", "downloads",
                          "userinfo", "userbrowse", "network_filter", "buddies", "statistics", "pluginhandler",
                          "chatrooms", "privatechat", "notifications"}, isolated_mode=True)
    config.sections["server"].update(server=(host, int(port)), login="reference", passw="local-test-only",
                                     upnp=False, auto_connect_startup=False)
    config.sections["userinfo"].update(descr=repr("Nicotine reference 世界"), pic=str(state / "picture.png"))
    config.sections["transfers"].update(shared=[], buddyshared=[], trustedshared=[], remotedownloads=False)
    outgoing = state / "outgoing" / "Album"
    outgoing.mkdir(parents=True)
    offered_names = ("blocked.txt", "one.txt", "世界.txt", "empty", "revoked.txt",
                     "buddy.txt", "trusted.txt", "untrusted.txt", "filtered.blocked", "resume.bin")
    for name in offered_names:
        (outgoing / name).write_bytes(b"" if name == "empty" else ("reference offer " + name).encode())
    (outgoing / "resume.bin").write_bytes(b"resume-test\n" * 65536)
    config.sections["transfers"]["shared"] = [("Reference", str(outgoing.parent))]
    config.sections["transfers"].update(uploaddir=str(state / "received"),
                                       incompletedir=str(state / "incomplete"))
    core.cli_interface_address = "127.0.0.1"
    core.cli_listen_port = int(listen_port)
    events.connect("server-login", logged_in)
    events.connect("user-info-response", received_profile)
    events.connect("shared-file-list-response", received_shares)
    core.start()
    core.connect()
    requested = False
    requested_shares = 0
    stopping = False
    receiving_enabled = False
    requested_send = 0
    sending_files = []
    deadline = time.monotonic() + 45
    while events.process_thread_events():
        if (state / "request").exists() and not requested:
            requested = True
            core.userinfo.show_user("terminal", refresh=True)
        browse_request = state / "browse-request.json"
        if browse_request.exists():
            sequence = json.loads(browse_request.read_text())
            if not isinstance(sequence, int) or not 1 <= sequence <= 100:
                raise RuntimeError("invalid local browse sequence")
            if sequence != requested_shares:
                requested_shares = sequence
                core.userbrowse.browse_user("terminal", new_request=True)
        if (state / "enable-receiving").exists() and not receiving_enabled:
            core.buddies.add_buddy("terminal")
            config.sections["transfers"].update(remotedownloads=True, uploadallowed=2)
            receiving_enabled = True
            publish("receiving-ready.json", True)
        send_request = state / "send-request.json"
        if send_request.exists() and not core.shares.rescanning:
            request = json.loads(send_request.read_text())
            if request["sequence"] != requested_send:
                if not isinstance(request["sequence"], int) or not 1 <= request["sequence"] <= 10:
                    raise RuntimeError("invalid local send sequence")
                if not request["files"] or any(name not in offered_names for name in request["files"]):
                    raise RuntimeError("invalid local send selection")
                requested_send = request["sequence"]
                sending_files = ["Reference\\Album\\" + name for name in request["files"]]
                for virtual_path in sending_files:
                    core.uploads.enqueue_upload("terminal", virtual_path)
            publish(f"send-{requested_send}.json", {
                name: getattr(core.uploads.transfers.get("terminal" + name), "status", "missing")
                for name in sending_files
            })
        if (state / "stop").exists() and not stopping:
            stopping = True
            core.quit()
        if time.monotonic() > deadline:
            raise RuntimeError("reference profile scenario timed out")
        time.sleep(0.02)
    if not stopping:
        raise RuntimeError("reference peer stopped before test completion")
