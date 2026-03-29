param(
    [switch]$Start
)

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

function Ensure-FileContainsLine {
    param(
        [string]$Path,
        [string]$Prefix,
        [string]$Line
    )

    $content = Get-Content $Path -Raw
    if ($content -match "(?m)^$([regex]::Escape($Prefix))") {
        $content = [regex]::Replace($content, "(?m)^$([regex]::Escape($Prefix)).*$", $Line)
    } else {
        $content = $content.TrimEnd() + "`r`n" + $Line + "`r`n"
    }
    Set-Content -Path $Path -Value $content -NoNewline
}

$repoRoot = Get-RepoRoot
$dockerDir = Join-Path $repoRoot "docker"
$envPath = Join-Path $dockerDir ".env"
$envExamplePath = Join-Path $dockerDir ".env.example"

if (-not (Test-Path $envPath)) {
    Copy-Item $envExamplePath $envPath
}

$cfg = Read-DockerEnv -Path $envPath
$synapseRuntime = Join-Path $dockerDir "runtime/synapse"
$dendriteRuntime = Join-Path $dockerDir "runtime/dendrite"
$dendriteConfigPath = Join-Path $dendriteRuntime "dendrite.yaml"

New-Item -ItemType Directory -Force -Path $synapseRuntime | Out-Null
New-Item -ItemType Directory -Force -Path $dendriteRuntime | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $dendriteRuntime "media") | Out-Null

if (-not (Test-Path (Join-Path $synapseRuntime "homeserver.yaml"))) {
    docker run --rm `
        -e "SYNAPSE_SERVER_NAME=$($cfg.SYNAPSE_SERVER_NAME)" `
        -e "SYNAPSE_REPORT_STATS=no" `
        -v "${synapseRuntime}:/data" `
        $cfg.SYNAPSE_IMAGE generate | Out-Host
}

$synapseConfigPath = Join-Path $synapseRuntime "homeserver.yaml"
Ensure-FileContainsLine -Path $synapseConfigPath -Prefix "registration_shared_secret:" -Line "registration_shared_secret: `"$($cfg.SYNAPSE_REGISTRATION_SHARED_SECRET)`""
Ensure-FileContainsLine -Path $synapseConfigPath -Prefix "enable_registration:" -Line "enable_registration: false"
Ensure-FileContainsLine -Path $synapseConfigPath -Prefix "public_baseurl:" -Line "public_baseurl: `"http://localhost:$($cfg.SYNAPSE_PORT)/`""

if (-not (Test-Path (Join-Path $dendriteRuntime "matrix_key.pem")) -or -not (Test-Path (Join-Path $dendriteRuntime "server.crt")) -or -not (Test-Path (Join-Path $dendriteRuntime "server.key"))) {
    docker run --rm `
        --entrypoint generate-keys `
        -v "${dendriteRuntime}:/etc/dendrite" `
        $cfg.DENDRITE_IMAGE `
        --private-key /etc/dendrite/matrix_key.pem `
        --tls-cert /etc/dendrite/server.crt `
        --tls-key /etc/dendrite/server.key `
        --server $cfg.DENDRITE_SERVER_NAME | Out-Host
}

$dendriteConfigLines = docker run --rm `
    --entrypoint generate-config `
    $cfg.DENDRITE_IMAGE `
    -ci `
    -server $cfg.DENDRITE_SERVER_NAME `
    -dir /etc/dendrite

$dendriteConfig = [string]::Join("`n", $dendriteConfigLines)
$dendriteConfig = $dendriteConfig `
    -replace 'registration_disabled:\s*false', 'registration_disabled: true' `
    -replace 'registration_shared_secret:\s*complement', ('registration_shared_secret: ' + $cfg.DENDRITE_REGISTRATION_SHARED_SECRET) `
    -replace 'guests_disabled:\s*false', 'guests_disabled: true'

Set-Content -Path $dendriteConfigPath -Value $dendriteConfig

if ($Start) {
    docker compose --env-file $envPath -f (Join-Path $dockerDir "compose.yaml") up -d | Out-Host
}

Write-Host "Docker runtime prepared in $dockerDir"
