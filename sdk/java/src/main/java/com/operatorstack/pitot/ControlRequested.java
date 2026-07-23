package com.operatorstack.pitot;

import com.fasterxml.jackson.annotation.*;

public class ControlRequested {
    private String actionID;
    private Object data;
    private String kind;
    private String pitotVersion;
    private String type;

    @JsonProperty("action_id")
    public String getActionID() { return actionID; }
    @JsonProperty("action_id")
    public void setActionID(String value) { this.actionID = value; }

    @JsonProperty("data")
    public Object getData() { return data; }
    @JsonProperty("data")
    public void setData(Object value) { this.data = value; }

    @JsonProperty("kind")
    public String getKind() { return kind; }
    @JsonProperty("kind")
    public void setKind(String value) { this.kind = value; }

    @JsonProperty("pitot_version")
    public String getPitotVersion() { return pitotVersion; }
    @JsonProperty("pitot_version")
    public void setPitotVersion(String value) { this.pitotVersion = value; }

    @JsonProperty("type")
    public String getType() { return type; }
    @JsonProperty("type")
    public void setType(String value) { this.type = value; }
}
