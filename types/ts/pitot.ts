export interface Pitot {
    boundary_fault:    BoundaryFault;
    control_requested: ControlRequested;
    control_response:  ControlResponse;
    event:             Event;
}

export interface BoundaryFault {
    action_id?:    string;
    host:          string;
    pitot_version: string;
    reason:        string;
    type:          string;
}

export interface ControlRequested {
    action_id:     string;
    data?:         unknown;
    kind:          string;
    pitot_version: string;
    type:          string;
}

export interface ControlResponse {
    action_id:     string;
    controller_id: string;
    message?:      string;
    outcome:       string;
    pitot_version: string;
    type:          string;
}

export interface Event {
    action?:       Action;
    content?:      Content;
    host:          Host;
    id?:           string;
    observation:   Observation;
    pitot_version: string;
    session_id?:   string;
    time?:         string;
    type:          string;
    usage?:        Usage;
}

export interface Action {
    id:   string;
    kind: string;
}

export interface Content {
    full?:   unknown;
    mode:    string;
    sha256?: string;
}

export interface Host {
    adapter_version?: string;
    name:             string;
}

export interface Observation {
    fidelity: string;
    source:   string;
}

export interface Usage {
    input_tokens:  number;
    output_tokens: number;
}
