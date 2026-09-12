#!/usr/bin/env python3
"""Cross-check frozen fixture bytes with the pinned, unmodified Nicotine+ codec.

No oto encoder is imported. No network connections or application startup occur.
Missing references, revision drift, and fixture mismatches are hard failures.
"""
import json
from pathlib import Path
import subprocess
import sys

sys.dont_write_bytecode = True
root = Path(__file__).resolve().parents[1]
reference = Path(sys.argv[1]).resolve() if len(sys.argv) > 1 else root / "nicotine-plus"
corpus = json.loads((root / "internal/testutil/testdata/social.json").read_text())
revision = subprocess.check_output(["git", "-C", str(reference), "rev-parse", "HEAD"], text=True).strip()
assert revision == corpus["nicotine_revision"], "Nicotine+ revision mismatch"
assert not subprocess.check_output([
    "git", "-C", str(reference), "status", "--porcelain", "--untracked-files=no"
]), "reference has local modifications"
sys.path.insert(0, str(reference))
from pynicotine import slskmessages as messages  # noqa: E402


def plain(value):
    """Make the reference's slotted roster entries comparable with frozen JSON."""
    if isinstance(value, messages.UserData):
        return {key: getattr(value, key) for key in value.__slots__}
    if isinstance(value, list):
        return [plain(item) for item in value]
    return value

names = set()
for fixture in corpus["fixtures"]:
    name = fixture["name"]
    assert name not in names, f"duplicate fixture: {name}"
    names.add(name)
    cls = getattr(messages, fixture["class"])
    direction = fixture["direction"]
    codes = messages.PEER_MESSAGE_CODES if direction == "peer" else messages.SERVER_MESSAGE_CODES
    assert codes[cls] == fixture["code"], name
    payload = bytes.fromhex(fixture["payload_hex"])
    if direction in ("client", "peer"):
        actual = cls(**fixture.get("arguments", {})).make_network_message()
        assert actual == payload, f"{name}: reference encoder differs"
    if direction in ("server", "peer"):
        message = cls(msg_content=memoryview(payload))
        message.parse_network_message()
        for key, value in fixture["expected"].items():
            assert plain(getattr(message, key)) == value, f"{name}: field {key} differs"
        assert not message.has_remaining_content(), f"{name}: unconsumed bytes"
print(f"Verified {len(names)} independent wire fixtures against Nicotine+ {revision}")
