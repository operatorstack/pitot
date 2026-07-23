package com.operatorstack.pitot;

import com.fasterxml.jackson.annotation.*;

public class ControlResponse {
    private String actionID;
    private String controllerID;
    private String message;
    private String outcome;
    private String pitotVersion;
    private String type;

    @JsonProperty("action_id")
    public String getActionID() { return actionID; }
    @JsonProperty("action_id")
    public void setActionID(String value) { this.actionID = value; }

    @JsonProperty("controller_id")
    public String getControllerID() { return controllerID; }
    @JsonProperty("controller_id")
    public void setControllerID(String value) { this.controllerID = value; }

    @JsonProperty("message")
    public String getMessage() { return message; }
    @JsonProperty("message")
    public void setMessage(String value) { this.message = value; }

    @JsonProperty("outcome")
    public String getOutcome() { return outcome; }
    @JsonProperty("outcome")
    public void setOutcome(String value) { this.outcome = value; }

    @JsonProperty("pitot_version")
    public String getPitotVersion() { return pitotVersion; }
    @JsonProperty("pitot_version")
    public void setPitotVersion(String value) { this.pitotVersion = value; }

    @JsonProperty("type")
    public String getType() { return type; }
    @JsonProperty("type")
    public void setType(String value) { this.type = value; }
}
