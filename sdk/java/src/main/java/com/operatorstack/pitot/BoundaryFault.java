package com.operatorstack.pitot;

import com.fasterxml.jackson.annotation.*;

public class BoundaryFault {
    private String actionID;
    private String host;
    private String pitotVersion;
    private String reason;
    private String type;

    @JsonProperty("action_id")
    public String getActionID() { return actionID; }
    @JsonProperty("action_id")
    public void setActionID(String value) { this.actionID = value; }

    @JsonProperty("host")
    public String getHost() { return host; }
    @JsonProperty("host")
    public void setHost(String value) { this.host = value; }

    @JsonProperty("pitot_version")
    public String getPitotVersion() { return pitotVersion; }
    @JsonProperty("pitot_version")
    public void setPitotVersion(String value) { this.pitotVersion = value; }

    @JsonProperty("reason")
    public String getReason() { return reason; }
    @JsonProperty("reason")
    public void setReason(String value) { this.reason = value; }

    @JsonProperty("type")
    public String getType() { return type; }
    @JsonProperty("type")
    public void setType(String value) { this.type = value; }
}
