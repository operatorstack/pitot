package com.operatorstack.pitot;

import com.fasterxml.jackson.annotation.*;

public class Types {
    private BoundaryFault boundaryFault;
    private ControlRequested controlRequested;
    private ControlResponse controlResponse;
    private Event event;

    @JsonProperty("boundary_fault")
    public BoundaryFault getBoundaryFault() { return boundaryFault; }
    @JsonProperty("boundary_fault")
    public void setBoundaryFault(BoundaryFault value) { this.boundaryFault = value; }

    @JsonProperty("control_requested")
    public ControlRequested getControlRequested() { return controlRequested; }
    @JsonProperty("control_requested")
    public void setControlRequested(ControlRequested value) { this.controlRequested = value; }

    @JsonProperty("control_response")
    public ControlResponse getControlResponse() { return controlResponse; }
    @JsonProperty("control_response")
    public void setControlResponse(ControlResponse value) { this.controlResponse = value; }

    @JsonProperty("event")
    public Event getEvent() { return event; }
    @JsonProperty("event")
    public void setEvent(Event value) { this.event = value; }
}
