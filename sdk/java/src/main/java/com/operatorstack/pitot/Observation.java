package com.operatorstack.pitot;

import com.fasterxml.jackson.annotation.*;

public class Observation {
    private String fidelity;
    private String source;

    @JsonProperty("fidelity")
    public String getFidelity() { return fidelity; }
    @JsonProperty("fidelity")
    public void setFidelity(String value) { this.fidelity = value; }

    @JsonProperty("source")
    public String getSource() { return source; }
    @JsonProperty("source")
    public void setSource(String value) { this.source = value; }
}
