// Example code that deserializes and serializes the model.
// extern crate serde;
// #[macro_use]
// extern crate serde_derive;
// extern crate serde_json;
//
// use generated_module::types;
//
// fn main() {
//     let json = r#"{"answer": 42}"#;
//     let model: types = serde_json::from_str(&json).unwrap();
// }

use serde::{Serialize, Deserialize};

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Types {
    pub boundary_fault: BoundaryFault,

    pub control_requested: ControlRequested,

    pub control_response: ControlResponse,

    pub event: Event,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BoundaryFault {
    pub action_id: Option<String>,

    pub host: String,

    pub pitot_version: String,

    pub reason: String,

    #[serde(rename = "type")]
    pub boundary_fault_type: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ControlRequested {
    pub action_id: String,

    pub data: Option<serde_json::Value>,

    pub kind: String,

    pub pitot_version: String,

    #[serde(rename = "type")]
    pub control_requested_type: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ControlResponse {
    pub action_id: String,

    pub controller_id: String,

    pub message: Option<String>,

    pub outcome: String,

    pub pitot_version: String,

    #[serde(rename = "type")]
    pub control_response_type: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Event {
    pub action: Option<Action>,

    pub content: Option<Content>,

    pub host: Host,

    pub id: Option<String>,

    pub observation: Observation,

    pub pitot_version: String,

    pub session_id: Option<String>,

    pub time: Option<String>,

    #[serde(rename = "type")]
    pub event_type: String,

    pub usage: Option<Usage>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Action {
    pub id: String,

    pub kind: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Content {
    pub full: Option<serde_json::Value>,

    pub mode: String,

    pub sha256: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Host {
    pub adapter_version: Option<String>,

    pub name: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Observation {
    pub fidelity: String,

    pub source: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Usage {
    pub input_tokens: i64,

    pub output_tokens: i64,
}
