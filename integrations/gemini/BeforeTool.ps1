param(
    [string]$Pitot = $(if ($env:PITOT_BIN) { $env:PITOT_BIN } else { "pitot" }),
    [string]$RealBin = "",
    [string]$Receipt = "",
    [string]$Nonce = "",
    [string]$Runtime = ""
)
$payload = [Console]::In.ReadToEnd()
$OutputEncoding = [Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)
$arguments = @()
if ($RealBin) { $arguments += @("--real-bin", $RealBin, "--receipt", $Receipt, "--nonce", $Nonce) }
$arguments += @("hook", "gemini")
if ($Runtime) { $arguments += @("--runtime", $Runtime) }
$startInfo = New-Object System.Diagnostics.ProcessStartInfo
$startInfo.FileName = $Pitot
$startInfo.Arguments = (($arguments | ForEach-Object { '"' + $_.Replace('"', '\"') + '"' }) -join ' ')
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
    @{ decision = "allow" } | ConvertTo-Json -Compress
} else {
    if (-not $pitotOutput) { $pitotOutput = "Pitot rejected the shell request" }
    if ($pitotOutput.Length -gt 1024) { $pitotOutput = $pitotOutput.Substring(0, 1024) }
    @{ decision = "deny"; reason = $pitotOutput } | ConvertTo-Json -Compress
}
exit 0
