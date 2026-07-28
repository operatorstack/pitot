$payload = [Console]::In.ReadToEnd()
$OutputEncoding = [Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)
$pitot = if ($env:PITOT_BIN) { $env:PITOT_BIN } else { "pitot" }
$startInfo = New-Object System.Diagnostics.ProcessStartInfo
$startInfo.FileName = $pitot
$startInfo.Arguments = '"hook" "copilot"'
$startInfo.UseShellExecute = $false
$startInfo.CreateNoWindow = $true
$startInfo.RedirectStandardInput = $true
$startInfo.StandardInputEncoding = [Text.UTF8Encoding]::new($false)
$startInfo.RedirectStandardOutput = $true
$startInfo.RedirectStandardError = $true
$process = New-Object System.Diagnostics.Process
$process.StartInfo = $startInfo
[void]$process.Start()
$payloadBytes = [Text.UTF8Encoding]::new($false).GetBytes($payload)
$stdin = $process.StandardInput.BaseStream
$stdin.Write($payloadBytes, 0, $payloadBytes.Length)
$stdin.Close()
$stdout = $process.StandardOutput.ReadToEnd()
$stderr = $process.StandardError.ReadToEnd()
$process.WaitForExit()
$pitotOutput = ($stdout + $stderr).Trim()
if ($process.ExitCode -eq 0) {
    @{ permissionDecision = "allow"; permissionDecisionReason = "Pitot accepted the shell action" } | ConvertTo-Json -Compress
} else {
    if (-not $pitotOutput) { $pitotOutput = "Pitot rejected the shell request" }
    if ($pitotOutput.Length -gt 1024) { $pitotOutput = $pitotOutput.Substring(0, 1024) }
    @{ permissionDecision = "deny"; permissionDecisionReason = $pitotOutput } | ConvertTo-Json -Compress
}
exit 0
