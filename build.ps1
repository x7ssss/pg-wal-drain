# Build script for pg-wal-drain (cross-compiles static binaries under 10MB)

$ErrorActionPreference = "Stop"

$Targets = @(
    @{ OS = "windows"; Arch = "amd64"; Output = "dist/pg-wal-drain-windows-amd64.exe" },
    @{ OS = "linux";   Arch = "amd64"; Output = "dist/pg-wal-drain-linux-amd64" },
    @{ OS = "linux";   Arch = "arm64"; Output = "dist/pg-wal-drain-linux-arm64" },
    @{ OS = "darwin";  Arch = "amd64"; Output = "dist/pg-wal-drain-darwin-amd64" },
    @{ OS = "darwin";  Arch = "arm64"; Output = "dist/pg-wal-drain-darwin-arm64" }
)

if (-not (Test-Path "dist")) {
    New-Item -ItemType Directory -Path "dist" | Out-Null
}

Write-Host "Running tests..." -ForegroundColor Cyan
go test ./...

Write-Host "`nBuilding static binaries..." -ForegroundColor Cyan
foreach ($target in $Targets) {
    Write-Host "Compiling for $($target.OS)/$($target.Arch) -> $($target.Output)..."
    
    $env:CGO_ENABLED = "0"
    $env:GOOS = $target.OS
    $env:GOARCH = $target.Arch

    go build -trimpath -ldflags="-s -w" -o $target.Output ./cmd/pg-wal-drain

    $item = Get-Item $target.Output
    $sizeMB = [math]::Round($item.Length / 1MB, 2)
    Write-Host "  Finished: $($target.Output) ($sizeMB MB)" -ForegroundColor Green

    if ($item.Length -gt 10MB) {
        Write-Error "Binary size exceeded 10MB limit: $sizeMB MB"
    }
}

Remove-Item env:CGO_ENABLED -ErrorAction SilentlyContinue
Remove-Item env:GOOS -ErrorAction SilentlyContinue
Remove-Item env:GOARCH -ErrorAction SilentlyContinue

Write-Host "`nAll binaries successfully compiled to dist/ and verified under 10MB." -ForegroundColor Green
