#Requires -Version 7.0
[CmdletBinding()]
param(
    [string]$Pitot = $(if ($env:PITOT_BIN) { $env:PITOT_BIN } else { "pitot" }),
    [string]$RealBin = "",
    [string]$Receipt = "",
    [string]$Nonce = "",
    [string]$Runtime = "",
    [switch]$VerifyCarrier,
    [switch]$ObserveCarrier,
    [uint32]$TargetBridgePid = 0,
    [Parameter(ValueFromPipeline = $true)][AllowEmptyString()][string]$InputObject
)

begin {
    $encoding = [Text.UTF8Encoding]::new($false)
    [Console]::InputEncoding = $encoding
    [Console]::OutputEncoding = $encoding
    $OutputEncoding = $encoding
    $chunks = [Collections.Generic.List[string]]::new()
    function Bytes-Hash([byte[]]$Bytes) {
        $algorithm = [Security.Cryptography.SHA256]::Create()
        try { return [BitConverter]::ToString($algorithm.ComputeHash($Bytes)).Replace('-','').ToLowerInvariant() } finally { $algorithm.Dispose() }
    }
    function Require-Owned([string]$Path,[string]$Root,[bool]$Directory=$false) {
      if (-not [IO.Path]::IsPathFullyQualified($Root) -or -not [IO.Path]::IsPathFullyQualified($Path) -or $Root.StartsWith('\\') -or $Path.StartsWith('\\')) { throw 'local absolute ownership required' }
  $r=[IO.Path]::GetFullPath($Root).TrimEnd('\'); $p=[IO.Path]::GetFullPath($Path)
      if (-not $p.Equals($r,[StringComparison]::OrdinalIgnoreCase) -and -not $p.StartsWith($r+'\',[StringComparison]::OrdinalIgnoreCase)) { throw 'ownership' }
      $current=$p
      while ($current) {
        if (([IO.File]::GetAttributes($current) -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'reparse' }
        $parent=[IO.Directory]::GetParent($current); if (-not $parent) { break }; $current=$parent.FullName
      }
      $d=([IO.File]::GetAttributes($p) -band [IO.FileAttributes]::Directory) -ne 0
      if ($d -ne $Directory) { throw 'file kind' }; return $p
    }
    function Read-Owned([string]$Path,[string]$Root) {
      $p=Require-Owned $Path $Root
      if ([IO.FileInfo]::new($p).Length -gt 1048576) { throw 'size' }
      $s=[IO.File]::Open($p,[IO.FileMode]::Open,[IO.FileAccess]::Read,[IO.FileShare]::Read)
      $m=[IO.MemoryStream]::new(); $b=[byte[]]::new(8192)
      try {
        while (($n=$s.Read($b,0,$b.Length)) -gt 0) { if ($m.Length+$n -gt 1048576) { throw 'size' }; $m.Write($b,0,$n) }
        return ,$m.ToArray()
      } finally { $s.Dispose(); $m.Dispose() }
    }
    function One-Process([uint32]$ProcessId) {
      $rows=@(Get-CimInstance -ClassName Win32_Process -Filter "ProcessId=$ProcessId" -Property ProcessId,ParentProcessId,CreationDate,ExecutablePath,CommandLine -OperationTimeoutSec 1 -ErrorAction Stop)
      if ($rows.Count -ne 1 -or $rows[0].ProcessId -ne $ProcessId) { throw 'process unavailable' }; return $rows[0]
    }
    function Observe-Carrier([string]$Payload,[bool]$InputPrefixBomRemoved) {
      $utf8=[Text.UTF8Encoding]::new($false,$true)
      $inner=ConvertFrom-Json -InputObject $Payload -AsHashtable -ErrorAction Stop
      if ($inner -isnot [Collections.IDictionary] -or $inner.command -isnot [string]) { throw 'inner payload' }
      $clock=[Diagnostics.Stopwatch]::StartNew(); $phase=$null
      foreach ($c in @('allow','deny')) { if ($inner.command -ceq ($env:PITOT_CURSOR_ISSUED_COMMAND+' PITOT_'+$c.ToUpperInvariant()+' '+$Nonce)) { $phase=$c } }
      if (-not $phase -or $Nonce -notmatch '^[0-9a-f]{32}$' -or $env:PITOT_CURSOR_CANDIDATE -notmatch '^[0-9a-f]{40}$') { throw 'fixture identity' }
      $self=One-Process $TargetBridgePid; $outer=One-Process $self.ParentProcessId; $node=One-Process $outer.ParentProcessId
      if (-not $self.CreationDate -or -not $outer.CreationDate -or -not $node.CreationDate -or $outer.CreationDate -gt $self.CreationDate -or $node.CreationDate -gt $outer.CreationDate) { throw 'ancestry dates' }
      foreach ($p in @($self,$outer)) { if (-not $p.ExecutablePath -or -not ([IO.Path]::GetFullPath($p.ExecutablePath)).Equals([IO.Path]::GetFullPath($env:PITOT_CURSOR_PARENT_SHELL),[StringComparison]::OrdinalIgnoreCase)) { throw 'pwsh image' } }
      if (-not $node.ExecutablePath -or -not ([IO.Path]::GetFullPath($node.ExecutablePath)).Equals([IO.Path]::GetFullPath($env:PITOT_CURSOR_RELEASE_NODE),[StringComparison]::OrdinalIgnoreCase)) { throw 'released node' }
      $version=[Diagnostics.FileVersionInfo]::GetVersionInfo($outer.ExecutablePath)
      $hash=(Get-FileHash -LiteralPath $outer.ExecutablePath -Algorithm SHA256 -ErrorAction Stop).Hash.ToLowerInvariant()
      if ($version.FileMajorPart -lt 7 -or $hash -cne $env:PITOT_CURSOR_PARENT_SHELL_SHA256) { throw 'outer version/hash' }
      # Exact narrow vendor spawn grammar. Never execute parsed command line.
      $exe=[regex]::Escape($env:PITOT_CURSOR_PARENT_SHELL)
      $pattern='^\s*(?:"'+$exe+'"|'+$exe+')\s+-ExecutionPolicy\s+Bypass\s+(?:-NonInteractive\s+)?-File\s+(?:"(?<file>[^"\r\n]+)"|(?<file>[^"\s]+))\s*$'
      $a=[regex]::Match($outer.CommandLine,$pattern,[Text.RegularExpressions.RegexOptions]::IgnoreCase)
      if (-not $a.Success) { throw 'outer argv' }
      $scriptPath=Require-Owned $a.Groups['file'].Value $env:PITOT_CURSOR_CARRIER_TEMP
      if ([IO.Path]::GetFileName($scriptPath) -notmatch '^ps-script-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.ps1$') { throw 'carrier name' }
      $sb=Read-Owned $scriptPath $env:PITOT_CURSOR_CARRIER_TEMP
      if ($sb.Length -lt 3 -or $sb[0] -ne 239 -or $sb[1] -ne 187 -or $sb[2] -ne 191) { throw 'carrier BOM' }
      $script=$utf8.GetString($sb,3,$sb.Length-3)
      # The fixture command begins with &, which the vendor carrier preserves.
      $wrapper='\$OutputEncoding = \[System.Text.Encoding\]::UTF8; Get-Content -LiteralPath ''(?<path>(?:[^'']|'''')*)'' -Raw \| & \{ \$input \| '+[regex]::Escape($env:PITOT_CURSOR_VERIFICATION_HOOK_COMMAND)+' \}'
      $matches=[regex]::Matches($script,$wrapper)
      if ($matches.Count -ne 1) { throw 'exact wrapper' }
      $payloadPath=Require-Owned ($matches[0].Groups['path'].Value.Replace("''","'")) $env:PITOT_CURSOR_CARRIER_TEMP
      if ([IO.Path]::GetFileName($payloadPath) -cne 'payload.json' -or [IO.Path]::GetFileName([IO.Path]::GetDirectoryName($payloadPath)) -notmatch '^cursor-hooks-[A-Za-z0-9]+$') { throw 'payload name' }
      $pb=Read-Owned $payloadPath $env:PITOT_CURSOR_CARRIER_TEMP
      $original=ConvertFrom-Json -InputObject ($utf8.GetString($pb)) -AsHashtable -ErrorAction Stop
      if ($original -isnot [Collections.IDictionary] -or $original.command -isnot [string] -or $original.command -cne $inner.command) { throw 'command equality' }
      $nodeHash=(Get-FileHash -LiteralPath $node.ExecutablePath -Algorithm SHA256 -ErrorAction Stop).Hash.ToLowerInvariant()
      if ($nodeHash -cne $env:PITOT_CURSOR_RELEASE_NODE_SHA256) { throw 'released node hash' }
      $record=@{schema_version=1;source_candidate_sha=$env:PITOT_CURSOR_CANDIDATE;nonce=$Nonce;phase=$phase;observed_command=$inner.command;expected_command=($env:PITOT_CURSOR_ISSUED_COMMAND+' PITOT_'+$phase.ToUpperInvariant()+' '+$Nonce);status='draft';elapsed_ms=$clock.ElapsedMilliseconds;owned_temp_root=$env:PITOT_CURSOR_CARRIER_TEMP;input_prefix_bom_removed=$InputPrefixBomRemoved;
        inner=@{pid=$self.ProcessId;parent_pid=$self.ParentProcessId;creation_date=$self.CreationDate.ToUniversalTime().ToString('o');version=$version.FileVersion;image_path=$self.ExecutablePath;image_sha256=$hash};
        parent=@{pid=$outer.ProcessId;parent_pid=$outer.ParentProcessId;creation_date=$outer.CreationDate.ToUniversalTime().ToString('o');image_path=$outer.ExecutablePath;argv=$outer.CommandLine;file_major_part=$version.FileMajorPart;file_version=$version.FileVersion;image_sha256=$hash};
        grandparent=@{pid=$node.ProcessId;creation_date=$node.CreationDate.ToUniversalTime().ToString('o');image_path=$node.ExecutablePath;image_sha256=$nodeHash;version_prefix=[IO.Path]::GetDirectoryName($node.ExecutablePath)};
        carrier=@{path=$scriptPath;bytes_base64=[Convert]::ToBase64String($sb);sha256=(Bytes-Hash $sb)};
        payload=@{path=$payloadPath;bytes_base64=[Convert]::ToBase64String($pb);sha256=(Bytes-Hash $pb)}}
      $root=Require-Owned $env:PITOT_CURSOR_CARRIER_ROOT $env:PITOT_CURSOR_CARRIER_ROOT $true
      $dest=[IO.Path]::Combine($root,"$Nonce-$phase.draft.json"); $tmp=[IO.Path]::Combine($root,"$Nonce-$phase-$([Guid]::NewGuid()).tmp")
      $bytes=$utf8.GetBytes(($record|ConvertTo-Json -Depth 12 -Compress))
      $f=[IO.File]::Open($tmp,[IO.FileMode]::CreateNew,[IO.FileAccess]::Write,[IO.FileShare]::None)
      try {$f.Write($bytes,0,$bytes.Length);$f.Flush($true)} finally {$f.Dispose()}
      [IO.File]::Move($tmp,$dest) # no overwrite; one receipt per phase
    }
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
        $originalPayload = $payload
        if (($VerifyCarrier -or $ObserveCarrier) -and $encoding.GetByteCount($originalPayload) -gt 1048576) { throw 'Invalid carrier fixture input' }
        $inputPrefixBomRemoved = $payload.Length -gt 0 -and $payload[0] -eq [char]0xFEFF
        if ($inputPrefixBomRemoved) { $payload = $payload.Substring(1) }
        if ($ObserveCarrier) {
            try { Observe-Carrier $payload $inputPrefixBomRemoved; exit 0 } catch { [Console]::Error.WriteLine($_.Exception.Message); exit 1 }
        }
        if (-not $payload.Trim()) { throw "Cursor supplied no shell request" }
        $request = ConvertFrom-Json -InputObject $payload -AsHashtable -ErrorAction Stop
        if ($request -isnot [Collections.IDictionary]) { throw "Cursor supplied an invalid shell request" }

        $carrier = $null
        if ($VerifyCarrier) {
            if ($Nonce -notmatch '^[0-9a-f]{32}$' -or $encoding.GetByteCount($payload) -gt 1048576) { throw 'Invalid carrier fixture input' }
            $phase = if ($request.command -ceq ($env:PITOT_CURSOR_ISSUED_COMMAND + ' PITOT_ALLOW ' + $Nonce)) { 'allow' } elseif ($request.command -ceq ($env:PITOT_CURSOR_ISSUED_COMMAND + ' PITOT_DENY ' + $Nonce)) { 'deny' } else { throw 'Unexpected fixture command' }
            $root = Require-Owned $env:PITOT_CURSOR_CARRIER_ROOT $env:PITOT_CURSOR_CARRIER_ROOT $true
            $watch = [Diagnostics.Stopwatch]::StartNew()
            $observeStart = [Diagnostics.ProcessStartInfo]::new()
            $observeStart.FileName = $env:PITOT_CURSOR_PARENT_SHELL
            foreach ($argument in @('-NoLogo','-NoProfile','-NonInteractive','-File',$PSCommandPath,'-ObserveCarrier','-TargetBridgePid',[string]$PID,'-Nonce',$Nonce)) { $observeStart.ArgumentList.Add($argument) }
            $observeStart.UseShellExecute = $false
            $observeStart.RedirectStandardInput = $true
            $observeStart.StandardInputEncoding = $encoding
            $observeStart.RedirectStandardOutput = $true
            $observeStart.RedirectStandardError = $true
            $observer = [Diagnostics.Process]::new()
            $observer.StartInfo = $observeStart
            $observerStarted = $false
            try {
                [void]$observer.Start()
                $observerStarted = $true
                $observeOut = $observer.StandardOutput.ReadToEndAsync()
                $observeErr = $observer.StandardError.ReadToEndAsync()
                $inputWrite = $observer.StandardInput.WriteAsync($originalPayload)
                $inputBudget = 5000 - [int]$watch.ElapsedMilliseconds
                if ($inputBudget -le 0 -or -not $inputWrite.Wait($inputBudget)) { throw 'Carrier observer input deadline' }
                [void]$inputWrite.GetAwaiter().GetResult()
                $observer.StandardInput.Close()
                $remaining = 5000 - [int]$watch.ElapsedMilliseconds
                if ($remaining -le 0 -or -not $observer.WaitForExit($remaining)) { throw 'Carrier observer deadline' }
                if ($observer.ExitCode -ne 0) {
                    $errorText = $observeErr.GetAwaiter().GetResult()
                    if ($errorText.Length -gt 1024) { $errorText = $errorText.Substring(0,1024) }
                    $failureFile = [IO.Path]::Combine($root,"$Nonce-$phase-$([Guid]::NewGuid()).error.txt")
                    $failureBytes = $encoding.GetBytes($errorText)
                    $failureStream = [IO.File]::Open($failureFile,[IO.FileMode]::CreateNew,[IO.FileAccess]::Write,[IO.FileShare]::None)
                    try { $failureStream.Write($failureBytes,0,$failureBytes.Length) } finally { $failureStream.Dispose() }
                    throw ('Carrier observation failed: ' + $errorText.Trim())
                }
                if ($observeOut.GetAwaiter().GetResult().Trim() -or $observeErr.GetAwaiter().GetResult().Trim()) { throw 'Unexpected observer output' }
            } finally {
                if ($observerStarted -and -not $observer.HasExited) { $observer.Kill($true); [void]$observer.WaitForExit(1000) }
                $observer.Dispose()
            }
            $draft = [IO.Path]::Combine($root,"$Nonce-$phase.draft.json")
            $carrier = ConvertFrom-Json -InputObject ([Text.UTF8Encoding]::new($false,$true).GetString((Read-Owned $draft $root))) -AsHashtable -ErrorAction Stop
            if ($carrier.status -cne 'draft' -or $carrier.phase -cne $phase -or $carrier.nonce -cne $Nonce -or $carrier.observed_command -cne $request.command) { throw 'Carrier draft mismatch' }
            if ($carrier.input_prefix_bom_removed -isnot [bool] -or $carrier.input_prefix_bom_removed -ne $inputPrefixBomRemoved) { throw 'Carrier input prefix mismatch' }
        }
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
        $output = $stdout.GetAwaiter().GetResult()
        if ($VerifyCarrier) {
            $event = ConvertFrom-Json -InputObject $output -AsHashtable -ErrorAction Stop
            if ($event.type -cne 'action.requested' -or $event.host.name -cne 'cursor' -or $event.action.kind -cne 'shell' -or $event.action.id -notmatch '^act_' -or $event.content.mode -cne 'full' -or $event.content.full -cne $request.command) { throw 'Measured Cursor event mismatch' }
            $carrier.action_id = $event.action.id
            $carrier.status = 'observed'
            $destination = [IO.Path]::Combine($root,"$Nonce-$phase.json")
            $bytes = $encoding.GetBytes(($carrier | ConvertTo-Json -Depth 12 -Compress))
            $file = [IO.File]::Open($destination,[IO.FileMode]::CreateNew,[IO.FileAccess]::Write,[IO.FileShare]::None)
            try { $file.Write($bytes,0,$bytes.Length); $file.Flush($true) } finally { $file.Dispose() }
        }
        $reason = $stderr.GetAwaiter().GetResult().Trim()
        Write-CursorDecision ($process.ExitCode -eq 0) $reason
    } catch {
        Write-CursorDecision $false $_.Exception.Message
    } finally {
        if ($process) { $process.Dispose() }
    }
    exit 0
}
