package com.operatorstack.pitot;

import com.fasterxml.jackson.annotation.*;

public class Action {
    private String id;
    private String kind;

    @JsonProperty("id")
    public String getID() { return id; }
    @JsonProperty("id")
    public void setID(String value) { this.id = value; }

    @JsonProperty("kind")
    public String getKind() { return kind; }
    @JsonProperty("kind")
    public void setKind(String value) { this.kind = value; }
}
