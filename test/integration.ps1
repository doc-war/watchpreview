# integration.ps1 - Flat schema tests (ASCII-only for PS 5.1)
param(
    [string]$Exe = ".build\watchpreview.exe"
)

$ErrorActionPreference = "Stop"
$count = 0
$script:failures = 0

function Assert {
    param([string]$Message, [bool]$Condition)
    $script:count++
    if ($Condition) {
        Write-Host "[$($script:count)] PASS: $Message"
    } else {
        Write-Host "[$($script:count)] FAIL: $Message"
        $script:failures++
    }
}

function Invoke-WP {
    param([object[]]$Arguments)

    # 不能用 PS5.1 的 `2> file` 重定向：它把 stderr 变成格式化错误记录，
    # 还会按控制台宽度 wrap 长行（"stop" 会被拆断成 "st\r\nop'"），
    # 导致后续子串断言失真。这里用 .NET Process 原始字节捕获 stdout/stderr。
    $psi = New-Object System.Diagnostics.ProcessStartInfo
    $psi.FileName = $exe
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    $psi.UseShellExecute = $false
    $psi.CreateNoWindow = $true

    $parts = New-Object System.Collections.Generic.List[string]
    foreach ($a in $Arguments) {
        $q = [string]::Concat('"', $a.Replace('"', '\"'), '"')
        $parts.Add($q)
    }
    $psi.Arguments = [string]::Join(" ", $parts)

    $p = [System.Diagnostics.Process]::Start($psi)
    $outTask = $p.StandardOutput.ReadToEndAsync()
    $errTask = $p.StandardError.ReadToEndAsync()
    $p.WaitForExit()

    $outText = ($outTask.Result -replace "`r`n$", "").TrimEnd("`r", "`n")
    $errText = $errTask.Result.TrimEnd("`r", "`n")

    return @{
        ExitCode = $p.ExitCode
        Output   = $outText
        Stderr   = $errText
        Lines    = @($outText -split "`r?`n" | Where-Object { $_ -ne "" })
    }
}

function Wait-For {
    param([string]$Path, [int]$TimeoutMs = 8000)
    $deadline = (Get-Date).AddMilliseconds($TimeoutMs)
    while ((Get-Date) -lt $deadline) {
        if (Test-Path -LiteralPath $Path) { return $true }
        Start-Sleep -Milliseconds 200
    }
    return (Test-Path -LiteralPath $Path)
}

function Expand-Config {
    param([hashtable]$Obj, [string]$Path)
    $json = $Obj | ConvertTo-Json -Depth 6
    [System.IO.File]::WriteAllText($Path, $json, [System.Text.UTF8Encoding]::new($false))
}

# Resolve exe path
$here = Split-Path -Parent $MyInvocation.MyCommand.Definition
$root = Split-Path -Parent $here
if (-not (Test-Path -LiteralPath $Exe)) {
    $candidate = Join-Path $root $Exe
    if (Test-Path -LiteralPath $candidate) { $Exe = $candidate }
}
if (-not (Test-Path -LiteralPath $Exe)) { throw "exe not found: $Exe" }
$exe = (Resolve-Path -LiteralPath $Exe).Path

try {

# ---- Setup isolated work tree ----
$work = Join-Path $env:TEMP ("wp21-" + [Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $work | Out-Null
$live    = Join-Path $work "live"
$src     = Join-Path $work "src"
$cfg     = Join-Path $work "cfg"
$ignored = Join-Path $src "ignored"
New-Item -ItemType Directory -Path $live, $src, $cfg, $ignored | Out-Null
Set-Content -LiteralPath (Join-Path $live "index.html") -Value "<h1>wp21</h1>" -Encoding Ascii

# Isolate the state dir: watchpreview puts <id>.json under %LOCALAPPDATA%.
# Redirect it into the throwaway work tree so tests never evict/stop any
# real instance owned by the user (Part 8 starts up to 4 instances on purpose).
$state = Join-Path $work "state"
$env:LOCALAPPDATA = $state

# ==== Part 1: explicit --config, static-only (4-step contract) ====
Write-Host "`n--- Part 1: explicit --config (static-only) ---"
$cfgPath = Join-Path $cfg "preview.json"
Expand-Config -Obj @{ serveRoot = $live } -Path $cfgPath

$r1 = Invoke-WP @("preview", "--config", $cfgPath)
Assert "exit code is 0" ($r1.ExitCode -eq 0)
Assert "stdout is exactly one line" ($r1.Lines.Count -eq 1)
Assert "stdout is http url" ($r1.Output -match '^http://127\.0\.0\.1:\d+/$')
$url = $r1.Output

$r2 = Invoke-WP @("preview", "--config", $cfgPath)
Assert "idempotent returns same url" (($r2.ExitCode -eq 0) -and ($r2.Output -eq $url))

$r3 = Invoke-WP @("status", "--config", $cfgPath)
Assert "status contains url" (($r3.ExitCode -eq 0) -and ($r3.Output.Contains($url)))

$r4 = Invoke-WP @("stop", "--config", $cfgPath)
Assert "stop exit 0" ($r4.ExitCode -eq 0)
Assert "stop output says stopped" ($r4.Output -match 'stopped')

$r5 = Invoke-WP @("status", "--config", $cfgPath)
Assert "after stop, status reports not running" ($r5.Output -match 'not running')

# ==== Part 2: implicit cwd (no --config) ====
Write-Host "`n--- Part 2: implicit cwd ---"
Push-Location $live
try {
    $i1 = Invoke-WP @("preview")
    Assert "implicit preview exit 0" ($i1.ExitCode -eq 0)
    Assert "implicit single-line http url" (($i1.Lines.Count -eq 1) -and ($i1.Output -match '^http://127\.0\.0\.1:\d+/$'))
    $url2 = $i1.Output

    $i2 = Invoke-WP @("preview")
    Assert "implicit idempotent same url" ($i2.Output -eq $url2)

    $i3 = Invoke-WP @("status")
    Assert "implicit status contains url" (($i3.ExitCode -eq 0) -and ($i3.Output.Contains($url2)))

    $i4 = Invoke-WP @("stop")
    Assert "implicit stop" ($i4.Output -match 'stopped')
} finally {
    Pop-Location
}
$i5 = Invoke-WP @("status")
Assert "after implicit stop, not running" ($i5.Output -match 'not running')

# ==== Part 3: exclude filtering (ignored dirs must not trigger onChange) ====
Write-Host "`n--- Part 3: exclude filtering ---"
$okMarker  = Join-Path $work "ok.marker"
$trigger   = Join-Path $src  "trigger.txt"
$trigIgn   = Join-Path $ignored "ig.txt"

$cfgEx = Join-Path $cfg "ex.json"
Expand-Config -Obj @{
    serveRoot        = $live
    watch            = @($src)
    exclude          = @($ignored)
    onChangeCommand  = "cmd /c type NUL > $okMarker"
} -Path $cfgEx

$x1 = Invoke-WP @("preview", "--config", $cfgEx)
Assert "exclude: preview exit 0" ($x1.ExitCode -eq 0)
Start-Sleep -Milliseconds 500
Set-Content -LiteralPath $trigger -Value "t" -Encoding Ascii
Assert "exclude: source change triggers marker" (Wait-For -Path $okMarker -TimeoutMs 5000)
for ($tries = 0; $tries -lt 5; $tries++) {
    try { Remove-Item -LiteralPath $okMarker -Force; break } catch { Start-Sleep -Milliseconds 200 }
}
Set-Content -LiteralPath $trigIgn -Value "ig" -Encoding Ascii
Start-Sleep -Milliseconds 1500
Assert "exclude: ignored dir change does NOT trigger marker" (-not (Test-Path -LiteralPath $okMarker))
$x_stop = Invoke-WP @("stop", "--config", $cfgEx)
Assert "exclude: stop works" ($x_stop.Output -match 'stopped')

# ==== Part 4: onChangeCommand failure does NOT refresh ====
Write-Host "`n--- Part 4: onChangeCommand failure ---"
$failMarker = Join-Path $work "fail.marker"
$cfgFail    = Join-Path $cfg "fail.json"
Expand-Config -Obj @{
    serveRoot       = $live
    watch           = @($src)
    onChangeCommand = "cmd /c exit /b 7"
} -Path $cfgFail

$f1 = Invoke-WP @("preview", "--config", $cfgFail)
Assert "fail: preview exit 0" ($f1.ExitCode -eq 0)
Start-Sleep -Milliseconds 500
$trig2 = Join-Path $src "trigger2.txt"
Set-Content -LiteralPath $trig2 -Value "t2" -Encoding Ascii
Start-Sleep -Milliseconds 1500
Assert "fail: no success marker"  (-not (Test-Path -LiteralPath $okMarker))
Assert "fail: no fail marker"     (-not (Test-Path -LiteralPath $failMarker))
$f4 = Invoke-WP @("status", "--config", $cfgFail)
Assert "fail: instance still alive" ($f4.ExitCode -eq 0)

# ==== Part 5: sourceHash warning (reuse with different watch config) ====
Write-Host "`n--- Part 5: sourceHash warning ---"
$cfgHash = Join-Path $cfg "hash.json"
Expand-Config -Obj @{
    serveRoot       = $live
    watch           = @($src)
    onChangeCommand = "cmd /c type NUL > $failMarker"
} -Path $cfgHash

$h1 = Invoke-WP @("preview", "--config", $cfgHash)
Assert "hash: preview reuses existing url" (($h1.ExitCode -eq 0) -and ($h1.Output -eq $f1.Output))
Assert "hash: stderr warns to stop first" ($h1.Stderr.Contains("run 'stop'"))
$h_stop = Invoke-WP @("stop", "--config", $cfgHash)
Assert "hash: stop works" ($h_stop.Output -match 'stopped')
$h5 = Invoke-WP @("status", "--config", $cfgHash)
Assert "hash: after stop, not running" ($h5.Output -match 'not running')

# ==== Part 6: quoted paths in onChangeCommand (坑1: cmd /C + Go \" escaping) ====
# 旧实现直接 `cmd /C <命令>`，Go 会把引号转成 \"，cmd 不认反斜杠引号，
# 含引号路径的命令必然失败。修复后命令原样写入临时批处理，应能成功。
Write-Host "`n--- Part 6: quoted path in onChangeCommand ---"
$qqDir    = Join-Path $work "qq dir"
New-Item -ItemType Directory -Path $qqDir | Out-Null
$qMarker  = Join-Path $qqDir "q.marker"
$trigQ    = Join-Path $src "trigger-q.txt"

$cfgQ = Join-Path $cfg "quote.json"
Expand-Config -Obj @{
    serveRoot       = $live
    watch           = @($src)
    onChangeCommand = "cmd /c type NUL > `"$qMarker`""
} -Path $cfgQ

$q1 = Invoke-WP @("preview", "--config", $cfgQ)
Assert "quote: preview exit 0" ($q1.ExitCode -eq 0)
Start-Sleep -Milliseconds 500
Set-Content -LiteralPath $trigQ -Value "q" -Encoding Ascii
Assert "quote: oncompiled command with quoted path triggers marker" (Wait-For -Path $qMarker -TimeoutMs 5000)
$q_stop = Invoke-WP @("stop", "--config", $cfgQ)
Assert "quote: stop works" ($q_stop.Output -match 'stopped')

# ==== Part 7: CJK path in onChangeCommand (坑2: UTF-8 vs cmd ANSI/GBK) ====
# cmd 读批处理默认按 ANSI(GBK)，命令里 UTF-8 中文路径会乱码；
# watchpreview 写临时脚本首行放 chcp 65001，命令中的中文应正确解析。
Write-Host "`n--- Part 7: CJK path in onChangeCommand ---"
$cjkName  = [string][char]0x4E2D + [string][char]0x6587   # 中文
$cjkDir   = Join-Path $work $cjkName
New-Item -ItemType Directory -Path $cjkDir | Out-Null
$cMarker  = Join-Path $cjkDir "c.marker"
$trigC    = Join-Path $src "trigger-c.txt"

$cfgC = Join-Path $cfg "cjk.json"
Expand-Config -Obj @{
    serveRoot       = $live
    watch           = @($src)
    onChangeCommand = "cmd /c type NUL > `"$cMarker`""
} -Path $cfgC

$c1 = Invoke-WP @("preview", "--config", $cfgC)
Assert "cjk: preview exit 0" ($c1.ExitCode -eq 0)
Start-Sleep -Milliseconds 500
Set-Content -LiteralPath $trigC -Value "c" -Encoding Ascii
Assert "cjk: oncompiled command with CJK path triggers marker" (Wait-For -Path $cMarker -TimeoutMs 5000)
$c_stop = Invoke-WP @("stop", "--config", $cfgC)
Assert "cjk: stop works" ($c_stop.Output -match 'stopped')

# ==== Part 8: global capacity eviction (max 5 instances) ====
Write-Host "`n--- Part 8: capacity eviction (max 5 instances) ---"
foreach ($n in @("A","B","C","D","E","F")) {
    $d = Join-Path $work ("app" + $n)
    New-Item -ItemType Directory -Path $d | Out-Null
    Set-Content -LiteralPath (Join-Path $d "index.html") -Value $n -Encoding Ascii
}
$cfgA = Join-Path $cfg "capA.json"
$cfgB = Join-Path $cfg "capB.json"
$cfgC = Join-Path $cfg "capC.json"
$cfgD = Join-Path $cfg "capD.json"
$cfgE = Join-Path $cfg "capE.json"
$cfgF = Join-Path $cfg "capF.json"
Expand-Config -Obj @{ serveRoot = (Join-Path $work "appA") } -Path $cfgA
Expand-Config -Obj @{ serveRoot = (Join-Path $work "appB") } -Path $cfgB
Expand-Config -Obj @{ serveRoot = (Join-Path $work "appC") } -Path $cfgC
Expand-Config -Obj @{ serveRoot = (Join-Path $work "appD") } -Path $cfgD
Expand-Config -Obj @{ serveRoot = (Join-Path $work "appE") } -Path $cfgE
Expand-Config -Obj @{ serveRoot = (Join-Path $work "appF") } -Path $cfgF

$e1 = Invoke-WP @("preview","--config",$cfgA)
Assert "cap: preview A exit 0" ($e1.ExitCode -eq 0)
$e2 = Invoke-WP @("preview","--config",$cfgB)
Assert "cap: preview B exit 0" ($e2.ExitCode -eq 0)
$e3 = Invoke-WP @("preview","--config",$cfgC)
Assert "cap: preview C exit 0" ($e3.ExitCode -eq 0)
$e4 = Invoke-WP @("preview","--config",$cfgD)
Assert "cap: preview D exit 0" ($e4.ExitCode -eq 0)
$e5 = Invoke-WP @("preview","--config",$cfgE)
Assert "cap: preview E exit 0" ($e5.ExitCode -eq 0)

# 5 instances: all should be alive, no eviction
$stateDir = Join-Path $state "watchpreview"
$cfiles5 = @(Get-ChildItem -LiteralPath $stateDir -Filter *.json -ErrorAction SilentlyContinue)
Assert "cap: 5 instances running (no eviction)" ($cfiles5.Count -eq 5)

# 6th instance: wrapper evicts oldest (A) before forking F
$e6 = Invoke-WP @("preview","--config",$cfgF)
Assert "cap: preview F exit 0" ($e6.ExitCode -eq 0)
Assert "cap: F single-line http url" (($e6.Lines.Count -eq 1) -and ($e6.Output -match '^http://127\.0\.0\.1:\d+/$'))
Start-Sleep -Milliseconds 300

# oldest A was evicted
$deadline = (Get-Date).AddSeconds(5)
$notRunning = $false
while ((Get-Date) -lt $deadline) {
    $sA = Invoke-WP @("status","--config",$cfgA)
    if ($sA.Output -match 'not running') { $notRunning = $true; break }
    Start-Sleep -Milliseconds 300
}
Assert "cap: oldest A evicted (not running)" $notRunning

# B, C, D, E, F still running (status prints JSON including the url field)
foreach ($lb in @("B","C","D","E","F")) {
    $s = Invoke-WP @("status","--config",(Join-Path $cfg ("cap" + $lb + ".json")))
    Assert "cap: $lb still running" (($s.ExitCode -eq 0) -and ($s.Output.Contains('"url"')))
}

# exactly 5 instance files remain (A's removed on evict)
$cfiles = @(Get-ChildItem -LiteralPath $stateDir -Filter *.json -ErrorAction SilentlyContinue)
Assert "cap: exactly 5 live instance files remain" ($cfiles.Count -eq 5)

# cleanup
foreach ($cf in @($cfgB,$cfgC,$cfgD,$cfgE,$cfgF)) { Invoke-WP @("stop","--config",$cf) | Out-Null }

} finally {
    if (Test-Path -LiteralPath $work) { Remove-Item -LiteralPath $work -Recurse -Force }
}

# ---- Summary ----
Write-Host ""
if ($script:failures -gt 0) {
    Write-Host "CONTRACT FAILED: $($script:failures) failure(s) out of $($script:count) checks"
    exit 1
}
Write-Host "ALL CONTRACT CHECKS PASSED ($($script:count) checks)"
exit 0