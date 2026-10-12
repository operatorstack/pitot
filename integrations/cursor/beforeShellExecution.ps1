#Requires -Version 7.0
[CmdletBinding()]
param(
    [string]$Pitot = $(if ($env:PITOT_BIN) { $env:PITOT_BIN } else { "pitot" }),
    [string]$RealBin = "",
    [string]$Receipt = "",
    [string]$Nonce = "",
    [string]$Runtime = "",
    [Parameter(ValueFromPipeline = $true)][AllowEmptyString()][string]$InputObject
)

begin {
    $encoding = [Text.UTF8Encoding]::new($false)
    [Console]::InputEncoding = $encoding
    [Console]::OutputEncoding = $encoding
    $OutputEncoding = $encoding
    $chunks = [Collections.Generic.List[string]]::new()
}
process {
    if ($PSBoundParameters.ContainsKey("InputObject")) { $chunks.Add($InputObject) }
}
end {
    function Write-CursorDecision([bool]$Allowed, [string]$Reason = "") {
        $result = @{ continue = $true; permission = $(if ($Allowed) { "allow" } else { "deny" }) }
        if (-not $Allowed) {
            if (-not $Reason) { $Reason = "Pitot rejected the shell request" }
            if ($Reason.Length -gt 1024) { $Reason = $Reason.Substring(0, 1024) }
            $result.user_message = $Reason
            $result.agent_message = $Reason
        }
        $result | ConvertTo-Json -Compress
    }

    $process = $null
    try {
        $payload = if ($chunks.Count) { [string]::Join("`n", $chunks) } else { [Console]::In.ReadToEnd() }
        if (-not $payload.Trim()) { throw "Cursor supplied no shell request" }
        $request = ConvertFrom-Json -InputObject $payload -AsHashtable -ErrorAction Stop
        if ($request -isnot [Collections.IDictionary]) { throw "Cursor supplied an invalid shell request" }

        $arguments = @()
        if ($RealBin) { $arguments += @("--real-bin", $RealBin, "--receipt", $Receipt, "--nonce", $Nonce) }
        $arguments += @("hook", "cursor")
        if ($Runtime) { $arguments += @("--runtime", $Runtime) }
        $start = [Diagnostics.ProcessStartInfo]::new()
        $start.FileName = $Pitot
        foreach ($argument in $arguments) { $start.ArgumentList.Add($argument) }
        $start.UseShellExecute = $false
        $start.CreateNoWindow = $true
        $start.RedirectStandardInput = $true
        $start.StandardInputEncoding = $encoding
        $start.RedirectStandardOutput = $true
        $start.StandardOutputEncoding = $encoding
        $start.RedirectStandardError = $true
        $start.StandardErrorEncoding = $encoding
        $process = [Diagnostics.Process]::new()
        $process.StartInfo = $start
        [void]$process.Start()
        $stdout = $process.StandardOutput.ReadToEndAsync()
        $stderr = $process.StandardError.ReadToEndAsync()
        $process.StandardInput.Write($payload)
        $process.StandardInput.Close()
        if (-not $process.WaitForExit(10000)) {
            $process.Kill($true)
            $process.WaitForExit()
            throw "Pitot did not answer the shell request in time"
        }
        $reason = ($stdout.GetAwaiter().GetResult() + $stderr.GetAwaiter().GetResult()).Trim()
        Write-CursorDecision ($process.ExitCode -eq 0) $reason
    } catch {
        Write-CursorDecision $false $_.Exception.Message
    } finally {
        if ($process) { $process.Dispose() }
    }
    exit 0
}
