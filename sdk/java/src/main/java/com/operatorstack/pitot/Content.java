package com.operatorstack.pitot;

import com.fasterxml.jackson.annotation.*;

public class Content {
    private Object full;
    private String mode;
    private String sha256;

    @JsonProperty("full")
    public Object getFull() { return full; }
    @JsonProperty("full")
    public void setFull(Object value) { this.full = value; }

    @JsonProperty("mode")
    public String getMode() { return mode; }
    @JsonProperty("mode")
    public void setMode(String value) { this.mode = value; }

    @JsonProperty("sha256")
    public String getSha256() { return sha256; }
    @JsonProperty("sha256")
    public void setSha256(String value) { this.sha256 = value; }
}
