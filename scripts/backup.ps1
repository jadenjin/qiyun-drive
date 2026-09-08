param(
  [string]$Destination = (Join-Path (Split-Path -Parent $PSScriptRoot) "backups")
)

$ErrorActionPreference = "Stop"
$projectRoot = Split-Path -Parent $PSScriptRoot
$timestamp = Get-Date -Format "yyyyMMdd-HHmmssfff"
$backupDir = Join-Path $Destination "qiyun-$timestamp"

function Invoke-DockerToFile {
  param([string[]]$Arguments, [string]$OutputPath)
  $startInfo = [System.Diagnostics.ProcessStartInfo]::new()
  $startInfo.FileName = "docker"
  $startInfo.UseShellExecute = $false
  $startInfo.RedirectStandardOutput = $true
  $startInfo.RedirectStandardError = $true
  foreach ($argument in $Arguments) { $null = $startInfo.ArgumentList.Add($argument) }
  $process = [System.Diagnostics.Process]::new()
  $process.StartInfo = $startInfo
  if (-not $process.Start()) { throw "无法启动 Docker" }
  $stderrTask = $process.StandardError.ReadToEndAsync()
  $output = [System.IO.File]::Create($OutputPath)
  try { $process.StandardOutput.BaseStream.CopyTo($output) } finally { $output.Dispose() }
  $process.WaitForExit()
  $stderr = $stderrTask.GetAwaiter().GetResult()
  if ($process.ExitCode -ne 0) { throw "Docker 命令失败：$stderr" }
}

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
  throw "未找到 Docker，请先安装并启动 Docker Desktop。"
}

$null = New-Item -ItemType Directory -Path $backupDir -Force
$resolvedBackup = (Resolve-Path $backupDir).Path
Push-Location $projectRoot
$servicesToResume = @()
try {
  $runningServices = @(& docker compose ps --status running --services)
  if ($LASTEXITCODE -ne 0) { throw "无法读取 Compose 服务状态" }
  $rustfsContainer = (& docker compose ps -a -q rustfs).Trim()
  if ($LASTEXITCODE -ne 0 -or -not $rustfsContainer) { throw "无法定位实际 RustFS 容器" }
  $postgresContainer = (& docker compose ps -a -q postgres).Trim()
  if ($LASTEXITCODE -ne 0 -or -not $postgresContainer) { throw "无法定位实际数据库容器" }
  $backupImage = (& docker inspect --format '{{.Image}}' $postgresContainer).Trim()
  if ($LASTEXITCODE -ne 0 -or -not $backupImage) { throw "无法定位本机备份工具镜像" }
  # Existing presigned PUT URLs bypass the API. Freeze the object service too.
  $servicesToResume = @("caddy", "api", "worker", "rustfs") | Where-Object { $runningServices -contains $_ }
  if ($servicesToResume.Count -gt 0) {
    & docker compose stop @servicesToResume
    if ($LASTEXITCODE -ne 0) { throw "无法暂停 API 和 Worker" }
  }

  Invoke-DockerToFile -Arguments @("compose", "exec", "-T", "postgres", "pg_dump", "-U", "pan", "-d", "pan", "-Fc", "--no-owner", "--no-privileges") -OutputPath (Join-Path $resolvedBackup "postgres.dump")
  # Read the real mount from the stopped container, including device bind mounts.
  # A hardcoded volume name could silently back up an empty, unrelated volume.
  & docker run --rm --network none --read-only --security-opt no-new-privileges:true --user 0:0 --cap-drop ALL --cap-add DAC_READ_SEARCH --entrypoint sh --volumes-from "${rustfsContainer}:ro" -v "${resolvedBackup}:/backup" $backupImage -c "tar -C /data -czf /backup/rustfs-data.tar.gz ."
  if ($LASTEXITCODE -ne 0) { throw "无法备份 RustFS 数据卷" }

  $files = @("postgres.dump", "rustfs-data.tar.gz") | ForEach-Object {
    $itemPath = Join-Path $resolvedBackup $_
    $item = Get-Item -LiteralPath $itemPath
    [ordered]@{ name = $_; bytes = $item.Length; sha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $itemPath).Hash.ToLowerInvariant() }
  }
  [ordered]@{
    formatVersion = 1
    createdAt = (Get-Date).ToUniversalTime().ToString("o")
    composeProject = "qiyun-drive"
    files = $files
  } | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath (Join-Path $resolvedBackup "manifest.json") -Encoding utf8
  Write-Host "备份完成：$resolvedBackup"
} finally {
  if ($servicesToResume.Count -gt 0) { & docker compose start @servicesToResume }
  Pop-Location
}
