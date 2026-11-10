$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw "Go is not installed. Get it from https://go.dev/dl/"
}
go mod tidy
go run github.com/akavel/rsrc@v0.10.2 -arch amd64 -manifest app.manifest -ico icon.ico -o rsrc_windows_amd64.syso
New-Item -ItemType Directory -Force dist | Out-Null
$env:GOOS = "windows"; $env:GOARCH = "amd64"
go build -trimpath -ldflags "-s -w -H windowsgui" -o dist\brave-beta-portable.exe .
go build -trimpath -o dist\brave-beta-portable-debug.exe .
Write-Host "Done. Copy dist\brave-beta-portable.exe to an empty folder on your USB drive and run it."
