"""Private v1 wire admission, independent of the generated convenience types."""

import json
import re


def _object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("invalid envelope")
        result[key] = value
    return result


def _constant(_):
    raise ValueError("invalid envelope")


def _scalars(value):
    if isinstance(value, str):
        if any(0xD800 <= ord(char) <= 0xDFFF for char in value):
            raise ValueError("invalid envelope")
    elif isinstance(value, dict):
        for key, child in value.items():
            _scalars(key)
            _scalars(child)
    elif isinstance(value, list):
        for child in value:
            _scalars(child)


def parse(raw):
    if isinstance(raw, bytes):
        raw = raw.decode("utf-8", errors="strict")
    depth = 0
    quoted = False
    escaped = False
    for char in raw:
        if quoted:
            if escaped:
                escaped = False
            elif char == "\\":
                escaped = True
            elif char == '"':
                quoted = False
        elif char == '"':
            quoted = True
        elif char in "[{":
            depth += 1
            if depth > 128:
                raise ValueError("invalid envelope")
        elif char in "]}":
            depth -= 1
    value = json.loads(raw, object_pairs_hook=_object, parse_constant=_constant)
    _scalars(value)
    return value


def _shape(value, required, optional=()):
    if (
        not isinstance(value, dict)
        or not set(required) <= value.keys()
        or not value.keys() <= set(required) | set(optional)
    ):
        raise ValueError("invalid envelope")


def _string(value, nonempty=False):
    if not isinstance(value, str) or (nonempty and not value):
        raise ValueError("invalid envelope")


def _strings(value, fields, nonempty=False):
    for field in fields:
        if field in value:
            _string(value[field], nonempty)


def event(value):
    _shape(
        value,
        ("pitot_version", "type", "host", "observation"),
        ("id", "session_id", "time", "action", "content", "usage"),
    )
    if value["pitot_version"] != "1" or value["type"] not in (
        "action.requested",
        "model.usage",
    ):
        raise ValueError("invalid envelope")
    _strings(value, ("id", "session_id", "time"))
    host = value["host"]
    _shape(host, ("name",), ("adapter_version",))
    _strings(host, ("name",), True)
    _strings(host, ("adapter_version",))
    observation = value["observation"]
    _shape(observation, ("source", "fidelity"))
    if observation["source"] not in ("host_hook", "host_event") or observation[
        "fidelity"
    ] not in ("direct", "estimated"):
        raise ValueError("invalid envelope")
    if "action" in value:
        _shape(value["action"], ("id", "kind"))
        _strings(value["action"], ("id", "kind"), True)
    if "usage" in value:
        _shape(value["usage"], ("input_tokens", "output_tokens"))
        for count in value["usage"].values():
            if type(count) is not int or not 0 <= count <= 9007199254740991:
                raise ValueError("invalid envelope")
    if value["type"] == "action.requested" and "action" not in value:
        raise ValueError("invalid envelope")
    if value["type"] == "model.usage" and "usage" not in value:
        raise ValueError("invalid envelope")
    if "content" in value:
        content = value["content"]
        if not isinstance(content, dict):
            raise ValueError("invalid envelope")
        mode = content.get("mode")
        if mode == "full":
            _shape(content, ("mode", "full"))
            _string(content["full"])
        elif mode == "sha256":
            _shape(content, ("mode", "sha256"))
            if not isinstance(content["sha256"], str) or not re.fullmatch(
                r"[0-9a-fA-F]{64}", content["sha256"]
            ):
                raise ValueError("invalid envelope")
        elif mode == "omit":
            _shape(content, ("mode",))
        else:
            raise ValueError("invalid envelope")


def request(value):
    _shape(value, ("pitot_version", "type", "action_id", "kind"), ("data",))
    if value["pitot_version"] != "1" or value["type"] != "control.requested":
        raise ValueError("invalid envelope")
    _strings(value, ("action_id", "kind"), True)


def response(value):
    _scalars(value)
    _shape(
        value,
        ("pitot_version", "type", "controller_id", "action_id", "outcome"),
        ("message",),
    )
    if (
        value["pitot_version"] != "1"
        or value["type"] != "control.response"
        or value["outcome"] not in ("allow", "deny")
    ):
        raise ValueError("invalid envelope")
    _strings(value, ("controller_id", "action_id"), True)
    _strings(value, ("message",))
