use std::io::{self, BufRead};
use serde_json;

use crate::types::{Event, ControlRequested, ControlResponse};

pub type ConsumerHandler = Box<dyn Fn(Event) + Send + Sync>;

pub fn run_consumer<F>(handler: F)
where
    F: Fn(Event) + Send + Sync + 'static,
{
    let stdin = io::stdin();
    for line in stdin.lock().lines() {
        if let Ok(line_str) = line {
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
    for line in stdin.lock().lines() {
        if let Ok(line_str) = line {
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
                    if let Ok(json) = serde_json::to_string(&response) {
                        println!("{}", json);
                    }
                }
                Err(err) => {
                    eprintln!("Pitot Controller error: {}", err);
                }
            }
        }
    }
}
