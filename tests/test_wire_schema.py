"""Independent decoded-schema oracle and the separate JSON-Lines profile.

The standard Draft 2020-12 validator decides envelope semantics. Byte encoding,
unique member names, Unicode scalar strings, depth and counter spelling are
transport constraints; they cannot be claimed as JSON Schema validation.
"""

from __future__ import annotations

from decimal import Decimal
import json
from pathlib import Path
import unittest

from jsonschema import Draft202012Validator

ROOT = Path(__file__).resolve().parents[1]
SCHEMA = json.loads((ROOT / "schema" / "v1.json").read_text())
MAX_FRAME_BYTES = 4 * 1024 * 1024
MAX_DEPTH = 128


class TransportViolation(ValueError):
    pass


def reject_constant(value: str):
    raise ValueError("non-JSON number")


def mathematical_values(value):
    """Preserve exact numbers while presenting integral values to JSON Schema.

    JSON Schema considers 1.0 an integer. Decimal parsing avoids binary-float
    rounding around the interoperable counter limit before schema validation.
    """
    if isinstance(value, Decimal) and value == value.to_integral_value():
        return int(value)
    if isinstance(value, list):
        return [mathematical_values(item) for item in value]
    if isinstance(value, dict):
        return {key: mathematical_values(item) for key, item in value.items()}
    return value


def decoded_for_schema(raw: bytes):
    # Duplicate names deliberately use the decoder's last-member value here.
    # JSON Schema operates on decoded values, not on the original JSON tokens.
    return mathematical_values(
        json.loads(
            raw.decode("utf-8"), parse_float=Decimal, parse_constant=reject_constant
        )
    )


def unique_members(pairs):
    value = {}
    for key, item in pairs:
        if key in value:
            raise TransportViolation("duplicate object member")
        value[key] = item
    return value


def check_scalar_strings_and_depth(value, depth: int = 0):
    if isinstance(value, str):
        if any(0xD800 <= ord(char) <= 0xDFFF for char in value):
            raise TransportViolation("non-scalar Unicode string")
    elif isinstance(value, (dict, list)):
        depth += 1
        if depth > MAX_DEPTH:
            raise TransportViolation("frame depth exceeded")
        if isinstance(value, dict):
            for key, item in value.items():
                check_scalar_strings_and_depth(key, depth)
                check_scalar_strings_and_depth(item, depth)
        else:
            for item in value:
                check_scalar_strings_and_depth(item, depth)


def decoded_for_transport(raw: bytes):
    if raw.endswith(b"\n"):
        raw = raw[:-1]
    if len(raw) > MAX_FRAME_BYTES:
        raise TransportViolation("frame size exceeded")
    value = json.loads(
        raw.decode("utf-8"),
        parse_float=Decimal,
        parse_constant=reject_constant,
        object_pairs_hook=unique_members,
    )
    check_scalar_strings_and_depth(value)
    if not isinstance(value, dict):
        raise TransportViolation("top-level object required")
    if isinstance(value.get("usage"), dict):
        for key in ("input_tokens", "output_tokens"):
            # Decimal is retained for decimal/exponent spellings; bool is not
            # admitted even though Python bool is an int subclass.
            if key in value["usage"] and type(value["usage"][key]) is not int:
                raise TransportViolation("counter requires integer lexical spelling")
    return mathematical_values(value)


def validator_for(envelope: str):
    return Draft202012Validator(
        {
            "$schema": SCHEMA["$schema"],
            "$defs": SCHEMA["$defs"],
            "$ref": "#/$defs/" + envelope,
        }
    )


def wire_rows():
    return [
        json.loads(line)
        for line in (ROOT / "conformance" / "fixtures" / "wire.jsonl")
        .read_text()
        .splitlines()
    ]


class WireSchemaTests(unittest.TestCase):
    def test_schema_is_valid_draft_2020_12(self):
        Draft202012Validator.check_schema(SCHEMA)
        self.assertEqual(
            SCHEMA["$schema"], "https://json-schema.org/draft/2020-12/schema"
        )

    def test_every_existing_semantic_case_has_identical_schema_expectation(self):
        rows = {row["name"]: row for row in wire_rows()}
        original = [
            json.loads(line)
            for line in (ROOT / "conformance" / "fixtures" / "envelopes.jsonl")
            .read_text()
            .splitlines()
        ]
        self.assertEqual(len(original), 47)
        for case in original:
            with self.subTest(name=case["name"]):
                row = rows[case["name"]]
                self.assertEqual(json.loads(row["raw"]), case["message"])
                self.assertEqual(row["valid"], case["valid"])
                self.assertEqual(row["schema_valid"], case["valid"])
                self.assertEqual(
                    validator_for(case["envelope"]).is_valid(case["message"]),
                    case["valid"],
                )

    def test_shared_wire_corpus(self):
        rows = wire_rows()
        self.assertEqual(len({row["name"] for row in rows}), len(rows))
        for row in rows:
            with self.subTest(name=row["name"]):
                self.assertNotEqual("raw" in row, "raw_hex" in row)
                raw = (
                    row["raw"].encode("utf-8")
                    if "raw" in row
                    else bytes.fromhex(row["raw_hex"])
                )
                validator = validator_for(row["envelope"])
                try:
                    decoded = decoded_for_schema(raw)
                except (ValueError, UnicodeError):
                    self.assertIsNone(row["schema_valid"])
                else:
                    self.assertIsNotNone(row["schema_valid"])
                    self.assertEqual(validator.is_valid(decoded), row["schema_valid"])
                    # The public top-level schema must agree with its selected
                    # envelope definition on every successfully parsed record.
                    self.assertEqual(
                        Draft202012Validator(SCHEMA).is_valid(decoded),
                        row["schema_valid"],
                    )
                try:
                    admitted = validator.is_valid(decoded_for_transport(raw))
                except (ValueError, UnicodeError):
                    admitted = False
                self.assertEqual(admitted, row["valid"])

    def test_transport_only_rejections_are_not_schema_claims(self):
        names = {
            row["name"]
            for row in wire_rows()
            if row["schema_valid"] is True and not row["valid"]
        }
        self.assertTrue(
            {
                "wire-request-duplicate",
                "wire-escaped-duplicate",
                "wire-data-duplicate",
                "wire-lone-high-surrogate",
                "wire-counter-decimal",
                "wire-counter-exponent",
                "wire-depth-129",
            }.issubset(names)
        )

    def test_counter_schema_uses_exact_mathematical_values(self):
        validator = validator_for("usage")
        for spelling in ("1", "1.0", "1e0", "9007199254740991"):
            decoded = decoded_for_schema(
                (f'{{"input_tokens":{spelling},"output_tokens":0}}').encode()
            )
            self.assertTrue(validator.is_valid(decoded), spelling)
        for spelling in (
            "1.5",
            "9007199254740991.4",
            "9007199254740992",
            "9007199254740993",
        ):
            decoded = decoded_for_schema(
                (f'{{"input_tokens":{spelling},"output_tokens":0}}').encode()
            )
            self.assertFalse(validator.is_valid(decoded), spelling)

    def test_framing_size_and_depth_are_separate_from_schema(self):
        prefix = b'{"pitot_version":"1","type":"control.requested","kind":"x","action_id":"x","data":"'
        suffix = b'"}'
        at_limit = (
            prefix + b"x" * (MAX_FRAME_BYTES - len(prefix) - len(suffix)) + suffix
        )
        self.assertEqual(len(at_limit), MAX_FRAME_BYTES)
        self.assertTrue(
            validator_for("request").is_valid(decoded_for_transport(at_limit))
        )
        self.assertTrue(
            validator_for("request").is_valid(decoded_for_transport(at_limit + b"\n"))
        )
        with self.assertRaises(TransportViolation):
            decoded_for_transport(at_limit + b" ")


if __name__ == "__main__":
    unittest.main()
