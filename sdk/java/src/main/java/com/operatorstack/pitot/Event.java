package com.operatorstack.pitot;

import com.fasterxml.jackson.annotation.*;

public class Event {
    private Action action;
    private Content content;
    private Host host;
    private String id;
    private Observation observation;
    private String pitotVersion;
    private String sessionID;
    private String time;
    private String type;
    private Usage usage;

    @JsonProperty("action")
    public Action getAction() { return action; }
    @JsonProperty("action")
    public void setAction(Action value) { this.action = value; }

    @JsonProperty("content")
    public Content getContent() { return content; }
    @JsonProperty("content")
    public void setContent(Content value) { this.content = value; }

    @JsonProperty("host")
    public Host getHost() { return host; }
    @JsonProperty("host")
    public void setHost(Host value) { this.host = value; }

    @JsonProperty("id")
    public String getID() { return id; }
    @JsonProperty("id")
    public void setID(String value) { this.id = value; }

    @JsonProperty("observation")
    public Observation getObservation() { return observation; }
    @JsonProperty("observation")
    public void setObservation(Observation value) { this.observation = value; }

    @JsonProperty("pitot_version")
    public String getPitotVersion() { return pitotVersion; }
    @JsonProperty("pitot_version")
    public void setPitotVersion(String value) { this.pitotVersion = value; }

    @JsonProperty("session_id")
    public String getSessionID() { return sessionID; }
    @JsonProperty("session_id")
    public void setSessionID(String value) { this.sessionID = value; }

    @JsonProperty("time")
    public String getTime() { return time; }
    @JsonProperty("time")
    public void setTime(String value) { this.time = value; }

    @JsonProperty("type")
    public String getType() { return type; }
    @JsonProperty("type")
    public void setType(String value) { this.type = value; }

    @JsonProperty("usage")
    public Usage getUsage() { return usage; }
    @JsonProperty("usage")
    public void setUsage(Usage value) { this.usage = value; }
}
