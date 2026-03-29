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

    (Invoke-RestMethod -Method Post -Uri "$BaseUrl/_matrix/client/v3/login" -ContentType "application/json" -Body $payload).access_token
}

$repoRoot = Get-RepoRoot
$dockerDir = Join-Path $repoRoot "docker"
$envPath = Join-Path $dockerDir ".env"
$cfg = Read-DockerEnv -Path $envPath

$synapseBaseUrl = "http://localhost:$($cfg.SYNAPSE_PORT)"

& (Join-Path $PSScriptRoot "docker-bootstrap.ps1") -Start
& (Join-Path $PSScriptRoot "docker-seed.ps1")

$composeArgs = @("--env-file", $envPath, "-f", (Join-Path $dockerDir "compose.yaml"))
docker compose @composeArgs exec -T synapse register_new_matrix_user `
    -u alice `
    -p alicepass123 `
    -c /data/homeserver.yaml `
    http://localhost:8008 | Out-Host

$adminToken = Get-LoginToken -BaseUrl $synapseBaseUrl -User $cfg.SYNAPSE_ADMIN_USER -Password $cfg.SYNAPSE_ADMIN_PASSWORD
$aliceToken = Get-LoginToken -BaseUrl $synapseBaseUrl -User "alice" -Password "alicepass123"

$createRoomBody = @{
    name = "Smoke Test Room"
    topic = "Synapse to Dendrite smoke test"
} | ConvertTo-Json
$roomID = (Invoke-RestMethod -Method Post -Uri "$synapseBaseUrl/_matrix/client/v3/createRoom" -Headers @{ Authorization = "Bearer $adminToken" } -ContentType "application/json" -Body $createRoomBody).room_id

$inviteBody = @{ user_id = "@alice:$($cfg.SYNAPSE_SERVER_NAME)" } | ConvertTo-Json
Invoke-RestMethod -Method Post -Uri "$synapseBaseUrl/_matrix/client/v3/rooms/$([uri]::EscapeDataString($roomID))/invite" -Headers @{ Authorization = "Bearer $adminToken" } -ContentType "application/json" -Body $inviteBody | Out-Null
Invoke-RestMethod -Method Post -Uri "$synapseBaseUrl/_matrix/client/v3/join/$([uri]::EscapeDataString($roomID))" -Headers @{ Authorization = "Bearer $aliceToken" } -ContentType "application/json" -Body "{}" | Out-Null

go run ./cmd/migrate preflight --config docker/runtime/migration.yaml
go run ./cmd/migrate migrate --config docker/runtime/migration.yaml
go run ./cmd/migrate status --config docker/runtime/migration.yaml

Write-Host "Smoke test completed. State: docker/runtime/migration_state.json"
