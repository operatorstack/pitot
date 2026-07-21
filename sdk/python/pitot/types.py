from dataclasses import dataclass
from typing import Any, TypeVar, Type, cast


T = TypeVar("T")


def from_str(x: Any) -> str:
    assert isinstance(x, str)
    return x


def from_none(x: Any) -> Any:
    assert x is None
    return x


def from_union(fs, x):
    for f in fs:
        try:
            return f(x)
        except:
            pass
    assert False


def from_int(x: Any) -> int:
    assert isinstance(x, int) and not isinstance(x, bool)
    return x


def to_class(c: Type[T], x: Any) -> dict:
    assert isinstance(x, c)
    return cast(Any, x).to_dict()


@dataclass
class BoundaryFault:
    host: str
    pitot_version: str
    reason: str
    type: str
    action_id: str | None = None

    @staticmethod
    def from_dict(obj: Any) -> 'BoundaryFault':
        assert isinstance(obj, dict)
        host = from_str(obj.get("host"))
        pitot_version = from_str(obj.get("pitot_version"))
        reason = from_str(obj.get("reason"))
        type = from_str(obj.get("type"))
        action_id = from_union([from_str, from_none], obj.get("action_id"))
        return BoundaryFault(host, pitot_version, reason, type, action_id)

    def to_dict(self) -> dict:
        result: dict = {}
        result["host"] = from_str(self.host)
        result["pitot_version"] = from_str(self.pitot_version)
        result["reason"] = from_str(self.reason)
        result["type"] = from_str(self.type)
        if self.action_id is not None:
            result["action_id"] = from_union([from_str, from_none], self.action_id)
        return result


@dataclass
class ControlRequested:
    action_id: str
    kind: str
    pitot_version: str
    type: str
    data: Any = None

    @staticmethod
    def from_dict(obj: Any) -> 'ControlRequested':
        assert isinstance(obj, dict)
        action_id = from_str(obj.get("action_id"))
        kind = from_str(obj.get("kind"))
        pitot_version = from_str(obj.get("pitot_version"))
        type = from_str(obj.get("type"))
        data = obj.get("data")
        return ControlRequested(action_id, kind, pitot_version, type, data)

    def to_dict(self) -> dict:
        result: dict = {}
        result["action_id"] = from_str(self.action_id)
        result["kind"] = from_str(self.kind)
        result["pitot_version"] = from_str(self.pitot_version)
        result["type"] = from_str(self.type)
        if self.data is not None:
            result["data"] = self.data
        return result


@dataclass
class ControlResponse:
    action_id: str
    controller_id: str
    outcome: str
    pitot_version: str
    type: str
    message: str | None = None

    @staticmethod
    def from_dict(obj: Any) -> 'ControlResponse':
        assert isinstance(obj, dict)
        action_id = from_str(obj.get("action_id"))
        controller_id = from_str(obj.get("controller_id"))
        outcome = from_str(obj.get("outcome"))
        pitot_version = from_str(obj.get("pitot_version"))
        type = from_str(obj.get("type"))
        message = from_union([from_str, from_none], obj.get("message"))
        return ControlResponse(action_id, controller_id, outcome, pitot_version, type, message)

    def to_dict(self) -> dict:
        result: dict = {}
        result["action_id"] = from_str(self.action_id)
        result["controller_id"] = from_str(self.controller_id)
        result["outcome"] = from_str(self.outcome)
        result["pitot_version"] = from_str(self.pitot_version)
        result["type"] = from_str(self.type)
        if self.message is not None:
            result["message"] = from_union([from_str, from_none], self.message)
        return result


@dataclass
class Action:
    id: str
    kind: str

    @staticmethod
    def from_dict(obj: Any) -> 'Action':
        assert isinstance(obj, dict)
        id = from_str(obj.get("id"))
        kind = from_str(obj.get("kind"))
        return Action(id, kind)

    def to_dict(self) -> dict:
        result: dict = {}
        result["id"] = from_str(self.id)
        result["kind"] = from_str(self.kind)
        return result


@dataclass
class Content:
    mode: str
    full: Any = None
    sha256: str | None = None

    @staticmethod
    def from_dict(obj: Any) -> 'Content':
        assert isinstance(obj, dict)
        mode = from_str(obj.get("mode"))
        full = obj.get("full")
        sha256 = from_union([from_str, from_none], obj.get("sha256"))
        return Content(mode, full, sha256)

    def to_dict(self) -> dict:
        result: dict = {}
        result["mode"] = from_str(self.mode)
        if self.full is not None:
            result["full"] = self.full
        if self.sha256 is not None:
            result["sha256"] = from_union([from_str, from_none], self.sha256)
        return result


@dataclass
class Host:
    name: str
    adapter_version: str | None = None

    @staticmethod
    def from_dict(obj: Any) -> 'Host':
        assert isinstance(obj, dict)
        name = from_str(obj.get("name"))
        adapter_version = from_union([from_str, from_none], obj.get("adapter_version"))
        return Host(name, adapter_version)

    def to_dict(self) -> dict:
        result: dict = {}
        result["name"] = from_str(self.name)
        if self.adapter_version is not None:
            result["adapter_version"] = from_union([from_str, from_none], self.adapter_version)
        return result


@dataclass
class Observation:
    fidelity: str
    source: str

    @staticmethod
    def from_dict(obj: Any) -> 'Observation':
        assert isinstance(obj, dict)
        fidelity = from_str(obj.get("fidelity"))
        source = from_str(obj.get("source"))
        return Observation(fidelity, source)

    def to_dict(self) -> dict:
        result: dict = {}
        result["fidelity"] = from_str(self.fidelity)
        result["source"] = from_str(self.source)
        return result


@dataclass
class Usage:
    input_tokens: int
    output_tokens: int

    @staticmethod
    def from_dict(obj: Any) -> 'Usage':
        assert isinstance(obj, dict)
        input_tokens = from_int(obj.get("input_tokens"))
        output_tokens = from_int(obj.get("output_tokens"))
        return Usage(input_tokens, output_tokens)

    def to_dict(self) -> dict:
        result: dict = {}
        result["input_tokens"] = from_int(self.input_tokens)
        result["output_tokens"] = from_int(self.output_tokens)
        return result


@dataclass
class Event:
    host: Host
    observation: Observation
    pitot_version: str
    type: str
    action: Action | None = None
    content: Content | None = None
    id: str | None = None
    session_id: str | None = None
    time: str | None = None
    usage: Usage | None = None

    @staticmethod
    def from_dict(obj: Any) -> 'Event':
        assert isinstance(obj, dict)
        host = Host.from_dict(obj.get("host"))
        observation = Observation.from_dict(obj.get("observation"))
        pitot_version = from_str(obj.get("pitot_version"))
        type = from_str(obj.get("type"))
        action = from_union([Action.from_dict, from_none], obj.get("action"))
        content = from_union([Content.from_dict, from_none], obj.get("content"))
        id = from_union([from_str, from_none], obj.get("id"))
        session_id = from_union([from_str, from_none], obj.get("session_id"))
        time = from_union([from_str, from_none], obj.get("time"))
        usage = from_union([Usage.from_dict, from_none], obj.get("usage"))
        return Event(host, observation, pitot_version, type, action, content, id, session_id, time, usage)

    def to_dict(self) -> dict:
        result: dict = {}
        result["host"] = to_class(Host, self.host)
        result["observation"] = to_class(Observation, self.observation)
        result["pitot_version"] = from_str(self.pitot_version)
        result["type"] = from_str(self.type)
        if self.action is not None:
            result["action"] = from_union([lambda x: to_class(Action, x), from_none], self.action)
        if self.content is not None:
            result["content"] = from_union([lambda x: to_class(Content, x), from_none], self.content)
        if self.id is not None:
            result["id"] = from_union([from_str, from_none], self.id)
        if self.session_id is not None:
            result["session_id"] = from_union([from_str, from_none], self.session_id)
        if self.time is not None:
            result["time"] = from_union([from_str, from_none], self.time)
        if self.usage is not None:
            result["usage"] = from_union([lambda x: to_class(Usage, x), from_none], self.usage)
        return result


@dataclass
class Types:
    boundary_fault: BoundaryFault
    control_requested: ControlRequested
    control_response: ControlResponse
    event: Event

    @staticmethod
    def from_dict(obj: Any) -> 'Types':
        assert isinstance(obj, dict)
        boundary_fault = BoundaryFault.from_dict(obj.get("boundary_fault"))
        control_requested = ControlRequested.from_dict(obj.get("control_requested"))
        control_response = ControlResponse.from_dict(obj.get("control_response"))
        event = Event.from_dict(obj.get("event"))
        return Types(boundary_fault, control_requested, control_response, event)

    def to_dict(self) -> dict:
        result: dict = {}
        result["boundary_fault"] = to_class(BoundaryFault, self.boundary_fault)
        result["control_requested"] = to_class(ControlRequested, self.control_requested)
        result["control_response"] = to_class(ControlResponse, self.control_response)
        result["event"] = to_class(Event, self.event)
        return result


def types_from_dict(s: Any) -> Types:
    return Types.from_dict(s)


def types_to_dict(x: Types) -> Any:
    return to_class(Types, x)
