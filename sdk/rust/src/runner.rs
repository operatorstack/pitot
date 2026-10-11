use std::io::{self, BufRead, Write};

use crate::types::{ControlRequested, ControlResponse, Event};
use crate::wire;

const FRAME_LIMIT: usize = 4 * 1024 * 1024;

// Bound memory while draining rejected frames to the next LF. CR is a byte of
// the frame; final nonempty EOF records are accepted like newline-ended ones.
fn frame<R: BufRead>(reader: &mut R) -> io::Result<Option<Result<Vec<u8>, ()>>> {
    let mut value = Vec::new();
    let mut oversized = false;
    loop {
        let buffer = reader.fill_buf()?;
        if buffer.is_empty() {
            return if oversized {
                Ok(Some(Err(())))
            } else if value.is_empty() {
                Ok(None)
            } else {
                Ok(Some(Ok(value)))
            };
        }
        let lf = buffer.iter().position(|b| *b == b'\n');
        let count = lf.unwrap_or(buffer.len());
        if !oversized {
            if value.len() + count > FRAME_LIMIT {
                oversized = true;
                value.clear();
            } else {
                value.extend_from_slice(&buffer[..count]);
            }
        }
        reader.consume(count + usize::from(lf.is_some()));
        if lf.is_some() {
            return Ok(Some(if oversized { Err(()) } else { Ok(value) }));
        }
    }
}
fn blank(raw: &[u8]) -> bool {
    raw.iter()
        .all(|b| matches!(b, b' ' | b'\t' | b'\r' | b'\n'))
}

pub type ConsumerHandler = Box<dyn Fn(Event) + Send + Sync>;

pub fn run_consumer<F>(handler: F)
where
    F: Fn(Event) + Send + Sync + 'static,
{
    let stdin = io::stdin();
    let mut input = stdin.lock();
    loop {
        match frame(&mut input) {
            Ok(None) => break,
            Err(_) => {
                eprintln!("Pitot Consumer error");
                break;
            }
            Ok(Some(Err(()))) => eprintln!("Pitot Consumer error"),
            Ok(Some(Ok(raw))) => {
                if blank(&raw) {
                    continue;
                }
                let event = wire::parse(&raw).and_then(|mut v| {
                    wire::event_raw(&raw, &mut v)?;
                    serde_json::from_value::<Event>(v).map_err(|_| ())
                });
                match event {
                    Ok(event) => handler(event),
                    Err(()) => eprintln!("Pitot Consumer error"),
                }
            }
        }
    }
}

pub struct Outcome {
    pub outcome: String,
    pub message: Option<String>,
}

pub fn allow(message: Option<String>) -> Outcome {
    Outcome {
        outcome: "allow".to_string(),
        message,
    }
}

pub fn deny(message: Option<String>) -> Outcome {
    Outcome {
        outcome: "deny".to_string(),
        message,
    }
}

pub type ControllerHandler = Box<dyn Fn(ControlRequested) -> Outcome + Send + Sync>;

pub fn run_controller<F>(controller_id: &str, handler: F)
where
    F: Fn(ControlRequested) -> Outcome + Send + Sync + 'static,
{
    let stdin = io::stdin();
    let stdout = io::stdout();
    let mut input = stdin.lock();
    loop {
        let raw = match frame(&mut input) {
            Ok(None) => break,
            Err(_) => {
                eprintln!("Pitot Controller error");
                break;
            }
            Ok(Some(Err(()))) => {
                eprintln!("Pitot Controller error");
                continue;
            }
            Ok(Some(Ok(raw))) => raw,
        };
        if blank(&raw) {
            continue;
        }
        let request = wire::parse(&raw).and_then(|v| {
            wire::request(&v)?;
            serde_json::from_value::<ControlRequested>(v).map_err(|_| ())
        });
        let req = match request {
            Ok(req) => req,
            Err(()) => {
                eprintln!("Pitot Controller error");
                continue;
            }
        };
        let result = handler(req.clone());
        let response = ControlResponse {
            pitot_version: "1".to_string(),
            control_response_type: "control.response".to_string(),
            controller_id: controller_id.to_string(),
            action_id: req.action_id,
            outcome: result.outcome,
            message: result.message,
        };
        let encoded = serde_json::to_value(&response)
            .map_err(|_| ())
            .and_then(|mut value| {
                if response.message.is_none() {
                    value.as_object_mut().ok_or(())?.remove("message");
                }
                wire::response(&value)?;
                serde_json::to_string(&value).map_err(|_| ())
            });
        match encoded {
            Ok(json) if json.len() > FRAME_LIMIT => eprintln!("Pitot Controller error"),
            Ok(json) => {
                let mut output = stdout.lock();
                if writeln!(output, "{}", json).is_err() || output.flush().is_err() {
                    eprintln!("Pitot Controller error");
                }
            }
            Err(()) => eprintln!("Pitot Controller error"),
        }
    }
}
