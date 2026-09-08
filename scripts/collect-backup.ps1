param([string]$ConfigPath = "$env:LOCALAPPDATA\QiyunBackup\config.json")
$ErrorActionPreference = 'Stop'
$config = Get-Content -LiteralPath $ConfigPath -Raw | ConvertFrom-Json
$mutex = New-Object System.Threading.Mutex($false, 'Local\QiyunOffhostBackup')
$acquired = $false
try {
    $acquired = $mutex.WaitOne(0)
    if (-not $acquired) { throw 'An off-host backup collector is already running' }
    $credential = Import-Clixml -LiteralPath $config.credentialPath
    $env:QIYUN_SSH_PASSWORD = $credential.GetNetworkCredential().Password
    $env:PYTHONPATH = $config.pythonLibraries
    & $config.python (Join-Path $PSScriptRoot 'collect-backup.py') --target $config.target --known-hosts $config.knownHosts
    if ($LASTEXITCODE -ne 0) { throw 'Off-host backup collection failed' }
    & $config.python (Join-Path $PSScriptRoot 'backup-retention.py') $config.target --keep 14
    if ($LASTEXITCODE -ne 0) { throw 'Off-host backup retention failed' }
} finally {
    Remove-Item Env:QIYUN_SSH_PASSWORD -ErrorAction SilentlyContinue
    if ($acquired) { $mutex.ReleaseMutex() }
    $mutex.Dispose()
}
