param(
    [Parameter(Mandatory)][string]$Python,
    [Parameter(Mandatory)][string]$PythonLibraries,
    [Parameter(Mandatory)][string]$Target,
    [string]$KnownHosts = "$env:USERPROFILE\.ssh\known_hosts"
)
$ErrorActionPreference = 'Stop'
if (-not $env:QIYUN_SSH_PASSWORD) { throw 'Supply QIYUN_SSH_PASSWORD in the process environment for initial setup' }
$account = [System.Security.Principal.WindowsIdentity]::GetCurrent().Name
$settingsDirectory = Join-Path $env:LOCALAPPDATA 'QiyunBackup'
foreach ($directory in @($settingsDirectory, $Target)) {
    New-Item -ItemType Directory -Path $directory -Force | Out-Null
    & icacls.exe $directory /inheritance:r /grant:r ($account + ':(OI)(CI)F') | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Cannot restrict backup directory ACL' }
    foreach ($existing in @((Get-Acl -LiteralPath $directory).Access)) {
        if ($existing.IdentityReference.Value -ne $account) {
            & icacls.exe $directory /remove $existing.IdentityReference.Value | Out-Null
            if ($LASTEXITCODE -ne 0) { throw 'Cannot remove additional directory access' }
        }
    }
}
$securePassword = ConvertTo-SecureString $env:QIYUN_SSH_PASSWORD -AsPlainText -Force
$credentialPath = Join-Path $settingsDirectory 'credential.xml'
[System.Management.Automation.PSCredential]::new('root', $securePassword) | Export-Clixml -LiteralPath $credentialPath
$configPath = Join-Path $settingsDirectory 'config.json'
@{
    python = (Resolve-Path -LiteralPath $Python).Path
    pythonLibraries = (Resolve-Path -LiteralPath $PythonLibraries).Path
    target = (Resolve-Path -LiteralPath $Target).Path
    knownHosts = (Resolve-Path -LiteralPath $KnownHosts).Path
    credentialPath = $credentialPath
} | ConvertTo-Json | Set-Content -LiteralPath $configPath -Encoding UTF8
$runner = Join-Path $PSScriptRoot 'collect-backup.ps1'
$arguments = '-NoProfile -NonInteractive -WindowStyle Hidden -File "' + $runner + '" -ConfigPath "' + $configPath + '"'
$action = New-ScheduledTaskAction -Execute "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe" -Argument $arguments
$triggers = @(
    (New-ScheduledTaskTrigger -Daily -At '05:30'),
    (New-ScheduledTaskTrigger -AtLogOn -User $account)
)
$principal = New-ScheduledTaskPrincipal -UserId $account -LogonType Interactive -RunLevel Limited
$settings = New-ScheduledTaskSettingsSet -StartWhenAvailable -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -MultipleInstances IgnoreNew -ExecutionTimeLimit (New-TimeSpan -Hours 4) -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 15)
Register-ScheduledTask -TaskName 'Qiyun off-host backup' -Action $action -Trigger $triggers -Principal $principal -Settings $settings -Description 'Copy and SHA-256 verify Qiyun snapshots while this Windows user is logged in; credentials use Windows DPAPI.' -Force | Out-Null
Write-Output 'Installed daily and logon backup collection. Windows must be on and this user logged in.'
