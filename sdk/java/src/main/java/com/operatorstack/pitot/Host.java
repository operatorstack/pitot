package com.operatorstack.pitot;

import com.fasterxml.jackson.annotation.*;

public class Host {
    private String adapterVersion;
    private String name;

    @JsonProperty("adapter_version")
    public String getAdapterVersion() { return adapterVersion; }
    @JsonProperty("adapter_version")
    public void setAdapterVersion(String value) { this.adapterVersion = value; }

    @JsonProperty("name")
    public String getName() { return name; }
    @JsonProperty("name")
    public void setName(String value) { this.name = value; }
}
