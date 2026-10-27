[CmdletBinding()]
param(
    [string]$OutputDirectory = "dist"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$projectRoot = $PSScriptRoot
if (-not (Test-Path (Join-Path $projectRoot "go.mod") -PathType Leaf)) {
    throw "go.mod was not found in $projectRoot"
}

if (-not [System.IO.Path]::IsPathRooted($OutputDirectory)) {
    $OutputDirectory = Join-Path $projectRoot $OutputDirectory
}
$OutputDirectory = [System.IO.Path]::GetFullPath($OutputDirectory)
$outputPath = Join-Path $OutputDirectory "mikrotik-address-list-linux-amd64"

New-Item -ItemType Directory -Force -Path $OutputDirectory | Out-Null

$buildEnvironment = [ordered]@{
    GOOS        = "linux"
    GOARCH      = "amd64"
    CGO_ENABLED = "0"
    GOTOOLCHAIN = "auto"
}
$previousEnvironment = @{}
foreach ($name in $buildEnvironment.Keys) {
    $previousEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, "Process")
}

try {
    foreach ($name in $buildEnvironment.Keys) {
        [Environment]::SetEnvironmentVariable($name, $buildEnvironment[$name], "Process")
    }

    Push-Location $projectRoot
    try {
        & go build -trimpath -buildvcs=false "-ldflags=-s -w -buildid=" -o $outputPath .
        if ($LASTEXITCODE -ne 0) {
            throw "go build failed with exit code $LASTEXITCODE"
        }
    }
    finally {
        Pop-Location
    }
}
finally {
    foreach ($name in $previousEnvironment.Keys) {
        [Environment]::SetEnvironmentVariable($name, $previousEnvironment[$name], "Process")
    }
}

Write-Host "Built linux/amd64 binary: $outputPath"
