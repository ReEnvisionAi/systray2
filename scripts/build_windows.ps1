$envFile = Join-Path $PSScriptRoot "..\.env"
if (Test-Path $envFile) {
    Get-Content $envFile | ForEach-Object {
        if ($_ -match "^HF_TOKEN=(.+)") {
            $env:HF_TOKEN = $matches[1]
            Write-Host "HF_TOKEN set from .env file"
        }
    }
}

#!/powershell
#
# powershell -ExecutionPolicy Bypass -File .\scripts\build_windows.ps1
#

$ErrorActionPreference = "Stop"

function buildsetup() {
  $script:SRC_DIR = $PWD

  $inoSetup = (Get-Item "C:\Program Files*\Inno Setup*\")
  write-host $inoSetup
  if ($inoSetup.length -gt 0) {
    $script:INNO_SETUP_DIR = $inoSetup[0]
  }
  Write-Output "Checking version"
  if (!$env:VERSION) {
    $data = (git describe --tags --first-parent --abbrev=7 --long --dirty --always)
    $pattern = "v(.+)"
    if ($data -match $pattern) {
      $script:VERSION = $matches[1]
    }
  }
  else {
    $script:VERSION = $env:VERSION
  }
  $pattern = "(\d+[.]\d+[.]\d+).*"
  if ($script:VERSION -match $pattern) {
    $script:PKG_VERSION = $matches[1]
  }
  else {
    $script:PKG_VERSION = "0.0.0"
  }
  write-host "Building ReEnvision AI App $script:VERSION with package version $script:PKG_VERSION"

}

function buildApp() {
  write-host "Building ReEnvision AI App"
  set-location "${script:SRC_DIR}\app"
  & go-winres make
  #& windres -l 0 -o reai.syso reai.rc
  & go build -trimpath -ldflags "-s -w -H windowsgui -X=github.com/ReEnvision-AI/systray/version.Version=$script:VERSION" -o "${script:SRC_DIR}\dist\windows\ReEnvisionAI.exe" .
  if ($LASTEXITCODE -ne 0) {
    exit($LASTEXITCODE)
  }
  write-host "ReEnvision AI App built successfully"
}

function gatherDistributables() {
  write-host "Gathering distributables"
  $distDir = "${script:SRC_DIR}\win_files"
  $files = Get-ChildItem -Path $distDir -Recurse -File
  $files | ForEach-Object {
    $dest = Join-Path -Path "${script:SRC_DIR}\dist\windows" -ChildPath $_.Name
    Copy-Item -Path $_.FullName -Destination $dest -Force
  }

  # The installer bundles the Podman setup exe (referenced in install.iss).
  # It is not committed to the repo — download it when missing (CI, fresh clones).
  $podmanVersion = "5.4.1"
  $podmanExe = "${script:SRC_DIR}\dist\windows\podman-$podmanVersion-setup.exe"
  if (!(Test-Path $podmanExe)) {
    $podmanUrl = "https://github.com/containers/podman/releases/download/v$podmanVersion/podman-$podmanVersion-setup.exe"
    write-host "Downloading Podman installer from $podmanUrl"
    Invoke-WebRequest -Uri $podmanUrl -OutFile $podmanExe
  }

  write-host "Distributables gathered successfully"
}

function buildInstaller() {
  if ($null -eq ${script:INNO_SETUP_DIR}) {
    write-host "Inno Setup not present, skipping installer build"
    return
  }

  write-host "Building ReEnvision AI Installer"
  set-location "${script:SRC_DIR}\app"
  $env:PKG_VERSION = $script:PKG_VERSION
  Set-Location "${script:SRC_DIR}\app"
  $isccArgs = @(".\install.iss")
  if ($env:SKIP_SIGNING -eq "1") {
    write-host "SKIP_SIGNING=1 - building unsigned installer"
    $isccArgs = @("/DSKIP_SIGNING") + $isccArgs
  }
  & "${script:INNO_SETUP_DIR}\ISCC.exe" @isccArgs

  if ($LASTEXITCODE -ne 0) {
    exit($LASTEXITCODE)
  }
  write-host "ReEnvision AI Installer built successfully"
  write-host "Calculating SHA256 checksum"
  $installerPath = "${script:SRC_DIR}\dist\ReEnvisionAISetup.exe"
  $checksum = (Get-FileHash -Algorithm SHA256 -Path $installerPath).Hash
  Out-File -FilePath "${installerPath}.sha256" -InputObject $checksum
  write-host "SHA256 checksum saved to ${installerPath}.sha256"
}

buildsetup
try {
  buildApp
  gatherDistributables
  buildInstaller
}
catch {
  write-host "Build Failed"
  write-host $_
}
finally {
  set-location $script:SRC_DIR
  $env:PKG_VERSION = ""
  $env:HF_TOKEN = ""
}