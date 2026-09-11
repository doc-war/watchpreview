#requires -Version 5.1
<#
Contract guard test: verifies the upper-layer integration contract is intact.

Contract (see README "Integration Contract"):
  1. `watchpreview preview --root <dir>` on success -> exit 0, stdout has EXACTLY ONE line (the URL);
  2. second call with same root reuses the same URL (idempotent);
  3. `status` prints instance info; `stop` prints `preview stopped`, exit 0.

Usage:
  go build -o .build/watchpreview.exe ./src
  powershell -ExecutionPolicy Bypass -File test/integration.ps1 -Exe .build/watchpreview.exe

Note: this is the Windows guard; a .sh implementation for Linux/macOS follows the same contract.
NOTE: keep this file ASCII-only. PowerShell 5.1 reads BOM-less .ps1 as ANSI, so non-ASCII
comments could corrupt parsing on some machines. Do NOT add Chinese comments here.
#>
param(
    [Parameter(Mandatory = $true)][string]$Exe
)

function Assert-Contract {
    # Accept [object] and coerce explicitly: `-match` on an array returns the matched
    # lines (an array), not a [bool], which would fail a [bool] parameter cast.
    param([object]$Condition, [string]$Message)
    if (-not [bool]$Condition) {
        Write-Error "CONTRACT FAILED: $Message"
        exit 1
    }
}

$ErrorActionPreference = "Stop"

$exeAbs = (Resolve-Path -LiteralPath $Exe).Path
$work = Join-Path $env:TEMP ("wp-contract-" + [guid]::NewGuid().ToString("N"))
$dist = Join-Path $work "dist"
New-Item -ItemType Directory -Force -Path $dist | Out-Null
Set-Content -Path (Join-Path $dist "index.html") -Value "<html><head></head><body>integration</body></html>"

# Isolate state dir; do not touch the real %LOCALAPPDATA%\watchpreview
$oldLocalAppData = $env:LOCALAPPDATA
$env:LOCALAPPDATA = Join-Path $work "cache"

try {
    # 1. first start: exit 0, exactly one stdout line, starts with http://
    $err1 = Join-Path $work "stderr1.txt"
    $out1 = & $exeAbs preview --root $dist 2> $err1
    Assert-Contract ($LASTEXITCODE -eq 0) "preview exit code should be 0"

    $lines1 = @($out1)
    Assert-Contract ($lines1.Count -eq 1) "stdout must contain exactly one line, got: '$($lines1 -join ' | ')'"
    Assert-Contract ($lines1[0] -match '^http://') "stdout line must be a URL, got: '$($lines1[0])'"

    # Reuse detection requires the HTTP control endpoint to answer. There is a tiny
    # window between the state file being written and Serve() coming up; give it time
    # before testing idempotency, otherwise the fresh instance could be judged stale.
    Start-Sleep -Milliseconds 400

    # 2. second call: idempotent reuse, same URL, still one line
    $err2 = Join-Path $work "stderr2.txt"
    $out2 = & $exeAbs preview --root $dist 2> $err2
    Assert-Contract ($LASTEXITCODE -eq 0) "second preview exit code should be 0"

    $lines2 = @($out2)
    Assert-Contract ($lines2.Count -eq 1) "second preview stdout must be one line"
    Assert-Contract ($lines2[0] -eq $lines1[0]) "second preview must reuse same URL"

    # 3. status: exit 0, output contains the URL
    $err3 = Join-Path $work "stderr3.txt"
    $status = & $exeAbs status --root $dist 2> $err3
    Assert-Contract ($LASTEXITCODE -eq 0) "status exit code should be 0"
    Assert-Contract (($status -join "`n") -match [regex]::Escape($lines1[0])) "status output must contain the URL"

    # 4. stop: exit 0, output contains 'preview stopped'
    $err4 = Join-Path $work "stderr4.txt"
    $stop = & $exeAbs stop --root $dist 2> $err4
    Assert-Contract ($LASTEXITCODE -eq 0) "stop exit code should be 0"
    Assert-Contract (($stop -join "`n") -match "preview stopped") "stop output should contain 'preview stopped', got: '$stop'"

    Write-Output "ALL CONTRACT CHECKS PASSED"
}
finally {
    $env:LOCALAPPDATA = $oldLocalAppData
    Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
}