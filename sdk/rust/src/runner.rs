use std::io::{self, BufRead, Write};
use serde_json;

use crate::types::{Event, ControlRequested, ControlResponse};

pub type ConsumerHandler = Box<dyn Fn(Event) + Send + Sync>;

pub fn run_consumer<F>(handler: F)
where
    F: Fn(Event) + Send + Sync + 'static,
{
    let stdin = io::stdin();
    for line in stdin.lock().lines() {
        let line_str = match line {
            Ok(value) => value,
            Err(err) => {
                // Surface stdin read faults instead of silently dropping them.
                eprintln!("Pitot Consumer error: {}", err);
                continue;
            }
        };
        if line_str.trim().is_empty() {
            continue;
        }
        match serde_json::from_str::<Event>(&line_str) {
            Ok(event) => {
                handler(event);
            }
            Err(err) => {
                eprintln!("Pitot Consumer error: {}", err);
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
    for line in stdin.lock().lines() {
        let line_str = match line {
            Ok(value) => value,
            Err(err) => {
                eprintln!("Pitot Controller error: {}", err);
                continue;
            }
        };
        if line_str.trim().is_empty() {
            continue;
        }
        match serde_json::from_str::<ControlRequested>(&line_str) {
            Ok(req) => {
                let result = handler(req.clone());
                let response = ControlResponse {
                    pitot_version: "1".to_string(),
                    control_response_type: "control.response".to_string(),
                    controller_id: controller_id.to_string(),
                    action_id: req.action_id,
                    outcome: result.outcome,
                    message: result.message,
                };
                match serde_json::to_string(&response) {
                    Ok(json) => {
                        let mut handle = stdout.lock();
                        // stdout is block-buffered when piped (the normal case
                        // behind a runtime), so flush every response to keep the
                        // controller responsive.
                        if writeln!(handle, "{}", json).is_err() || handle.flush().is_err() {
                            eprintln!("Pitot Controller error: failed to write response");
                        }
                    }
                    Err(err) => {
                        eprintln!("Pitot Controller error: {}", err);
                    }
                }
            }
            Err(err) => {
                eprintln!("Pitot Controller error: {}", err);
            }
        }
    }
}
