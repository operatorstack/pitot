//! Private, fixed v1 wire admission before generated types are constructed.
use serde::de::{self, DeserializeSeed, MapAccess, SeqAccess, Visitor};
use serde_json::{Map, Number, Value};
use std::fmt;

struct Strict(usize);
impl<'de> DeserializeSeed<'de> for Strict {
    type Value = Value;
    fn deserialize<D: de::Deserializer<'de>>(self, deserializer: D) -> Result<Value, D::Error> {
        deserializer.deserialize_any(self)
    }
}
impl<'de> Visitor<'de> for Strict {
    type Value = Value;
    fn expecting(&self, f: &mut fmt::Formatter) -> fmt::Result {
        f.write_str("valid envelope")
    }
    fn visit_bool<E: de::Error>(self, v: bool) -> Result<Value, E> {
        Ok(Value::Bool(v))
    }
    fn visit_i64<E: de::Error>(self, v: i64) -> Result<Value, E> {
        Ok(Value::Number(v.into()))
    }
    fn visit_u64<E: de::Error>(self, v: u64) -> Result<Value, E> {
        Ok(Value::Number(v.into()))
    }
    fn visit_f64<E: de::Error>(self, v: f64) -> Result<Value, E> {
        Number::from_f64(v)
            .map(Value::Number)
            .ok_or_else(|| E::custom("invalid envelope"))
    }
    fn visit_str<E: de::Error>(self, v: &str) -> Result<Value, E> {
        Ok(Value::String(v.to_owned()))
    }
    fn visit_string<E: de::Error>(self, v: String) -> Result<Value, E> {
        Ok(Value::String(v))
    }
    fn visit_none<E: de::Error>(self) -> Result<Value, E> {
        Ok(Value::Null)
    }
    fn visit_unit<E: de::Error>(self) -> Result<Value, E> {
        Ok(Value::Null)
    }
    fn visit_seq<A: SeqAccess<'de>>(self, mut seq: A) -> Result<Value, A::Error> {
        if self.0 >= 128 {
            return Err(de::Error::custom("invalid envelope"));
        }
        let mut values = Vec::new();
        while let Some(value) = seq.next_element_seed(Strict(self.0 + 1))? {
            values.push(value);
        }
        Ok(Value::Array(values))
    }
    fn visit_map<A: MapAccess<'de>>(self, mut map: A) -> Result<Value, A::Error> {
        if self.0 >= 128 {
            return Err(de::Error::custom("invalid envelope"));
        }
        let mut values = Map::new();
        while let Some(key) = map.next_key::<String>()? {
            if values.contains_key(&key) {
                return Err(de::Error::custom("invalid envelope"));
            }
            values.insert(key, map.next_value_seed(Strict(self.0 + 1))?);
        }
        Ok(Value::Object(values))
    }
}

pub(crate) fn parse(raw: &[u8]) -> Result<Value, ()> {
    let mut decoder = serde_json::Deserializer::from_slice(raw);
    // The default decoder rejects depth 128. Our visitor owns the shared
    // transport limit and rejects before recursing beyond that exact boundary.
    decoder.disable_recursion_limit();
    let value = Strict(0).deserialize(&mut decoder).map_err(|_| ())?;
    decoder.end().map_err(|_| ())?;
    Ok(value)
}

// Read accounting lexemes separately: serde_json preserves decimal/exponent
// numbers as floats, but also represents the valid integer spelling -0 as a
// float. Only the two accounting fields are normalized; arbitrary request data
// retains the existing native codec representation.
pub(crate) fn event_raw(raw: &[u8], value: &mut Value) -> Result<(), ()> {
    #[derive(serde::Deserialize)]
    struct Envelope<'a> {
        #[serde(borrow)]
        usage: Option<&'a serde_json::value::RawValue>,
    }
    #[derive(serde::Deserialize)]
    struct Usage<'a> {
        #[serde(borrow)]
        input_tokens: &'a serde_json::value::RawValue,
        #[serde(borrow)]
        output_tokens: &'a serde_json::value::RawValue,
    }
    let mut decoder = serde_json::Deserializer::from_slice(raw);
    decoder.disable_recursion_limit();
    let envelope: Envelope = serde::Deserialize::deserialize(&mut decoder).map_err(|_| ())?;
    if let Some(usage) = envelope.usage {
        let fields: Usage = serde_json::from_str(usage.get()).map_err(|_| ())?;
        for (name, raw) in [
            ("input_tokens", fields.input_tokens),
            ("output_tokens", fields.output_tokens),
        ] {
            let token = raw.get();
            let digits = token.strip_prefix('-').unwrap_or(token);
            if digits.is_empty() || !digits.bytes().all(|c| c.is_ascii_digit()) {
                return Err(());
            }
            let n = token.parse::<i64>().map_err(|_| ())?;
            if !(0..=9007199254740991).contains(&n) {
                return Err(());
            }
            value
                .get_mut("usage")
                .and_then(Value::as_object_mut)
                .ok_or(())?
                .insert(name.to_owned(), Value::Number(n.into()));
        }
    }
    event(value)
}

fn shape<'a>(
    v: &'a Value,
    required: &[&str],
    optional: &[&str],
) -> Result<&'a Map<String, Value>, ()> {
    let object = v.as_object().ok_or(())?;
    if required.iter().any(|k| !object.contains_key(*k))
        || object
            .keys()
            .any(|k| !required.contains(&k.as_str()) && !optional.contains(&k.as_str()))
    {
        return Err(());
    }
    Ok(object)
}
fn string(v: &Value, nonempty: bool) -> Result<(), ()> {
    if v.as_str().is_some_and(|s| !nonempty || !s.is_empty()) {
        Ok(())
    } else {
        Err(())
    }
}
fn strings(o: &Map<String, Value>, keys: &[&str], nonempty: bool) -> Result<(), ()> {
    for key in keys {
        if let Some(v) = o.get(*key) {
            string(v, nonempty)?;
        }
    }
    Ok(())
}
fn equals(v: &Value, values: &[&str]) -> bool {
    v.as_str().is_some_and(|s| values.contains(&s))
}

pub(crate) fn event(v: &Value) -> Result<(), ()> {
    let o = shape(
        v,
        &["pitot_version", "type", "host", "observation"],
        &["id", "session_id", "time", "action", "content", "usage"],
    )?;
    if !equals(&o["pitot_version"], &["1"])
        || !equals(&o["type"], &["action.requested", "model.usage"])
    {
        return Err(());
    }
    strings(o, &["id", "session_id", "time"], false)?;
    let host = shape(&o["host"], &["name"], &["adapter_version"])?;
    strings(host, &["name"], true)?;
    strings(host, &["adapter_version"], false)?;
    let observation = shape(&o["observation"], &["source", "fidelity"], &[])?;
    if !equals(&observation["source"], &["host_hook", "host_event"])
        || !equals(&observation["fidelity"], &["direct", "estimated"])
    {
        return Err(());
    }
    if let Some(action) = o.get("action") {
        strings(shape(action, &["id", "kind"], &[])?, &["id", "kind"], true)?;
    }
    if let Some(usage) = o.get("usage") {
        for count in shape(usage, &["input_tokens", "output_tokens"], &[])?.values() {
            if !count.as_u64().is_some_and(|n| n <= 9007199254740991) {
                return Err(());
            }
        }
    }
    if equals(&o["type"], &["action.requested"]) && !o.contains_key("action")
        || equals(&o["type"], &["model.usage"]) && !o.contains_key("usage")
    {
        return Err(());
    }
    if let Some(content) = o.get("content") {
        match content.get("mode").and_then(Value::as_str) {
            Some("full") => {
                let c = shape(content, &["mode", "full"], &[])?;
                string(&c["full"], false)?;
            }
            Some("sha256") => {
                let c = shape(content, &["mode", "sha256"], &[])?;
                let hash = c["sha256"].as_str().ok_or(())?;
                if hash.len() != 64 || !hash.bytes().all(|b| b.is_ascii_hexdigit()) {
                    return Err(());
                }
            }
            Some("omit") => {
                shape(content, &["mode"], &[])?;
            }
            _ => return Err(()),
        }
    }
    Ok(())
}

pub(crate) fn request(v: &Value) -> Result<(), ()> {
    let o = shape(
        v,
        &["pitot_version", "type", "kind", "action_id"],
        &["data"],
    )?;
    if !equals(&o["pitot_version"], &["1"]) || !equals(&o["type"], &["control.requested"]) {
        return Err(());
    }
    strings(o, &["kind", "action_id"], true)
}
pub(crate) fn response(v: &Value) -> Result<(), ()> {
    let o = shape(
        v,
        &[
            "pitot_version",
            "type",
            "controller_id",
            "action_id",
            "outcome",
        ],
        &["message"],
    )?;
    if !equals(&o["pitot_version"], &["1"])
        || !equals(&o["type"], &["control.response"])
        || !equals(&o["outcome"], &["allow", "deny"])
    {
        return Err(());
    }
    strings(o, &["controller_id", "action_id"], true)?;
    strings(o, &["message"], false)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rejects_duplicate_decoded_members_and_invalid_scalar_strings() {
        for raw in [
            br#"{"data":{"a":1,"\u0061":2}}"#.as_slice(),
            br#""\ud800""#,
            br#"{} {}"#,
            b"\"\xff\"",
        ] {
            assert!(parse(raw).is_err());
        }
    }

    #[test]
    fn accounting_requires_safe_integer_lexemes() {
        for (token, accepted) in [
            ("0", true),
            ("-0", true),
            ("9007199254740991", true),
            ("9007199254740992", false),
            ("9007199254740991.1", false),
            ("1.0", false),
            ("1e0", false),
            ("true", false),
            ("-1", false),
        ] {
            let raw = format!(
                r#"{{"pitot_version":"1","type":"model.usage","host":{{"name":"test"}},"observation":{{"source":"host_event","fidelity":"direct"}},"usage":{{"input_tokens":{},"output_tokens":0}}}}"#,
                token
            );
            let mut value = parse(raw.as_bytes()).unwrap();
            assert_eq!(
                event_raw(raw.as_bytes(), &mut value).is_ok(),
                accepted,
                "{}",
                token
            );
        }
    }

    #[test]
    fn nesting_limit_is_exact() {
        for depth in [127, 128, 129] {
            let raw = format!("{}0{}", "[".repeat(depth), "]".repeat(depth));
            assert_eq!(
                parse(raw.as_bytes()).is_ok(),
                depth <= 128,
                "depth {}",
                depth
            );
        }
    }

    #[test]
    fn arbitrary_request_data_is_not_an_envelope() {
        let raw = br#"{"pitot_version":"1","type":"control.requested","action_id":"a","kind":"test","data":{"usage":{"input_tokens":1.25},"extra":null}}"#;
        assert!(request(&parse(raw).unwrap()).is_ok());
    }
}
