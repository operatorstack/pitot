package com.operatorstack.pitot;

import com.fasterxml.jackson.annotation.*;

public class Usage {
    private long inputTokens;
    private long outputTokens;

    @JsonProperty("input_tokens")
    public long getInputTokens() { return inputTokens; }
    @JsonProperty("input_tokens")
    public void setInputTokens(long value) { this.inputTokens = value; }

    @JsonProperty("output_tokens")
    public long getOutputTokens() { return outputTokens; }
    @JsonProperty("output_tokens")
    public void setOutputTokens(long value) { this.outputTokens = value; }
}
