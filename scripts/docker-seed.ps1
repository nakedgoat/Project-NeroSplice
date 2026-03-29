param()

$ErrorActionPreference = "Stop"

function Get-RepoRoot {
    Split-Path -Parent $PSScriptRoot
}

function Read-DockerEnv {
    param([string]$Path)
    $map = @{}
    foreach ($line in Get-Content $Path) {
        if ([string]::IsNullOrWhiteSpace($line) -or $line.Trim().StartsWith("#")) {
            continue
        }
        $parts = $line -split "=", 2
        if ($parts.Count -eq 2) {
            $map[$parts[0]] = $parts[1]
        }
    }
    return $map
}

function Wait-MatrixEndpoint {
    param(
        [string]$Url,
        [int]$MaxAttempts = 60
    )
    for ($i = 0; $i -lt $MaxAttempts; $i++) {
        try {
            Invoke-RestMethod -Method Get -Uri $Url | Out-Null
            return
        } catch {
            Start-Sleep -Seconds 2
        }
    }
    throw "Timed out waiting for $Url"
}

function Get-LoginToken {
    param(
        [string]$BaseUrl,
        [string]$User,
        [string]$Password
    )
    $payload = @{
        type = "m.login.password"
        identifier = @{
            type = "m.id.user"
            user = $User
        }
        password = $Password
    } | ConvertTo-Json -Depth 5

    $res = Invoke-RestMethod -Method Post -Uri "$BaseUrl/_matrix/client/v3/login" -ContentType "application/json" -Body $payload
    return $res.access_token
}

function Register-DendriteUser {
    param(
        [string]$BaseUrl,
        [string]$SharedSecret,
        [string]$Username,
        [string]$Password,
        [bool]$Admin
    )
    $nonce = (Invoke-RestMethod -Method Get -Uri "$BaseUrl/_synapse/admin/v1/register").nonce
    $hmac = [System.Security.Cryptography.HMACSHA1]::new([Text.Encoding]::UTF8.GetBytes($SharedSecret))
    $pieces = @(
        $nonce,
        [char]0,
        $Username,
        [char]0,
        $Password,
        [char]0,
        $(if ($Admin) { "admin" } else { "notadmin" })
    )
    $raw = [string]::Concat($pieces)
    $macBytes = $hmac.ComputeHash([Text.Encoding]::UTF8.GetBytes($raw))
    $mac = -join ($macBytes | ForEach-Object { $_.ToString("x2") })

    $payload = @{
        nonce = $nonce
        username = $Username
        password = $Password
        admin = $Admin
        mac = $mac
    } | ConvertTo-Json

    try {
        Invoke-RestMethod -Method Post -Uri "$BaseUrl/_synapse/admin/v1/register" -ContentType "application/json" -Body $payload | Out-Null
    } catch {
        $body = $_.ErrorDetails.Message
        if ($body -notmatch "taken|already exists|in use") {
            throw
        }
    }
}

$repoRoot = Get-RepoRoot
$dockerDir = Join-Path $repoRoot "docker"
$envPath = Join-Path $dockerDir ".env"
$cfg = Read-DockerEnv -Path $envPath

$composeArgs = @("--env-file", $envPath, "-f", (Join-Path $dockerDir "compose.yaml"))
docker compose @composeArgs up -d | Out-Host

$synapseBaseUrl = "http://localhost:$($cfg.SYNAPSE_PORT)"
$dendriteBaseUrl = "http://localhost:$($cfg.DENDRITE_CLIENT_PORT)"

Wait-MatrixEndpoint -Url "$synapseBaseUrl/_matrix/client/versions"
Wait-MatrixEndpoint -Url "$dendriteBaseUrl/_matrix/client/versions"

docker compose @composeArgs exec -T synapse register_new_matrix_user `
    -u $cfg.SYNAPSE_ADMIN_USER `
    -p $cfg.SYNAPSE_ADMIN_PASSWORD `
    -a `
    -c /data/homeserver.yaml `
    http://localhost:8008 | Out-Host

$synapseToken = Get-LoginToken -BaseUrl $synapseBaseUrl -User $cfg.SYNAPSE_ADMIN_USER -Password $cfg.SYNAPSE_ADMIN_PASSWORD

Register-DendriteUser `
    -BaseUrl $dendriteBaseUrl `
    -SharedSecret $cfg.DENDRITE_REGISTRATION_SHARED_SECRET `
    -Username $cfg.DENDRITE_ADMIN_USER `
    -Password $cfg.DENDRITE_ADMIN_PASSWORD `
    -Admin $true

$dendriteToken = Get-LoginToken -BaseUrl $dendriteBaseUrl -User $cfg.DENDRITE_ADMIN_USER -Password $cfg.DENDRITE_ADMIN_PASSWORD

$migrationConfig = @"
source:
  base_url: $synapseBaseUrl
  server_name: $($cfg.SYNAPSE_SERVER_NAME)
  access_token: "$synapseToken"
  insecure_skip_verify: false
target:
  base_url: $dendriteBaseUrl
  server_name: $($cfg.DENDRITE_SERVER_NAME)
  access_token: "$dendriteToken"
  registration_shared_secret: "$($cfg.DENDRITE_REGISTRATION_SHARED_SECRET)"
  insecure_skip_verify: false
migration:
  state_path: docker/runtime/migration_state.json
  password_report_path: docker/runtime/temp_passwords.csv
  concurrency: 2
  temp_password_prefix: migrated-
  user_limit: 0
  room_limit: 0
  media_limit: 0
"@
Set-Content -Path (Join-Path $dockerDir "runtime/migration.yaml") -Value $migrationConfig -NoNewline

Write-Host "Wrote docker/runtime/migration.yaml"
