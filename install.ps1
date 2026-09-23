# Installs deepcove into %LOCALAPPDATA%\deepcove\bin, or DEEPCOVE_INSTALL_DIR.
#
#   irm https://deepcove.dev/install.ps1 | iex
#
# DEEPCOVE_VERSION pins a release, such as v0.1.1. DEEPCOVE_NO_SERVICE=1 installs it without starting it
# in the background.
$ErrorActionPreference = 'Stop'
# The progress bar slows Invoke-WebRequest down many times over on Windows PowerShell.
$ProgressPreference = 'SilentlyContinue'

$repo = 'waltzofpearls/deepcove'
$api = "https://api.github.com/repos/$repo"

# Called on the last line, so nothing runs until the whole script has been read.
function Main {
    $installDir = if ($env:DEEPCOVE_INSTALL_DIR) { $env:DEEPCOVE_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'deepcove\bin' }
    if (-not [Environment]::Is64BitOperatingSystem) {
        throw 'deepcove needs 64-bit Windows'
    }

    $release = Get-Release
    $tag = $release.tag_name
    # There is no Windows on Arm build yet. Arm machines run this one under emulation.
    $archive = "deepcove_${tag}_windows-amd64.zip"
    Write-ReplacedVersion $installDir $tag

    $tmp = Join-Path ([IO.Path]::GetTempPath()) ([Guid]::NewGuid())
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {
        Write-Host "Downloading deepcove $tag for windows-amd64"
        $archivePath = Join-Path $tmp $archive
        $sumsPath = Join-Path $tmp 'SHA256SUMS'
        Save-ReleaseAsset $release $archive $archivePath
        Save-ReleaseAsset $release 'SHA256SUMS' $sumsPath
        Assert-Checksum $archivePath $sumsPath
        $target = Install-Binary $archivePath $tmp $installDir
    } finally {
        Remove-Item -Recurse -Force $tmp
    }
    Write-Host "Installed deepcove $tag to $target"

    $pathState = Add-ToUserPath $installDir
    $running = Start-DeepcoveService $installDir
    Write-NextSteps $installDir $pathState $running
}

function Write-ReplacedVersion([string] $installDir, [string] $tag) {
    $existing = Join-Path $installDir 'deepcove.exe'
    if (-not (Test-Path $existing)) {
        return
    }
    try {
        $line = & $existing version 2>$null | Select-String '^version:\s*(.+)$' | Select-Object -First 1
    } catch {
        # One that cannot run is replaced all the same, there is just nothing to say about it.
        return
    }
    if ($line) {
        $current = $line.Matches[0].Groups[1].Value.Trim()
        if ($current -ne $tag) {
            Write-Host "Replacing deepcove $current with $tag"
        }
    }
}

function Get-Headers {
    $headers = @{ 'User-Agent' = 'deepcove-install' }
    if ($env:GITHUB_TOKEN) {
        $headers['Authorization'] = "Bearer $env:GITHUB_TOKEN"
    }
    return $headers
}

function Get-Release {
    $url = if ($env:DEEPCOVE_VERSION) { "$api/releases/tags/$env:DEEPCOVE_VERSION" } else { "$api/releases/latest" }
    try {
        return Invoke-RestMethod -Uri $url -Headers (Get-Headers)
    } catch {
        throw 'no release found'
    }
}

function Save-ReleaseAsset($release, [string] $name, [string] $outFile) {
    $asset = $release.assets | Where-Object { $_.name -eq $name }
    if (-not $asset) {
        throw "release $($release.tag_name) has no $name"
    }
    $headers = Get-Headers
    if ($env:GITHUB_TOKEN) {
        # A private repository only hands out an asset at its API address.
        $headers['Accept'] = 'application/octet-stream'
        Invoke-WebRequest -Uri $asset.url -Headers $headers -OutFile $outFile
    } else {
        Invoke-WebRequest -Uri $asset.browser_download_url -Headers $headers -OutFile $outFile
    }
}

function Assert-Checksum([string] $archivePath, [string] $sumsPath) {
    $archive = Split-Path -Leaf $archivePath
    $line = Get-Content $sumsPath | Where-Object { ($_ -split '\s+')[1] -eq $archive }
    if (-not $line) {
        throw "SHA256SUMS does not list $archive"
    }
    $expected = ($line -split '\s+')[0]
    $actual = (Get-FileHash -Path $archivePath -Algorithm SHA256).Hash.ToLower()
    if ($actual -ne $expected) {
        throw "$archive does not match its checksum, so nothing was installed"
    }
}

function Install-Binary([string] $archivePath, [string] $tmp, [string] $installDir) {
    $unpacked = Join-Path $tmp 'unpacked'
    Expand-Archive -Path $archivePath -DestinationPath $unpacked
    $binary = Join-Path $unpacked 'deepcove.exe'
    if (-not (Test-Path $binary)) {
        throw "$(Split-Path -Leaf $archivePath) has no deepcove.exe inside"
    }

    New-Item -ItemType Directory -Force -Path $installDir | Out-Null
    $target = Join-Path $installDir 'deepcove.exe'
    try {
        Copy-Item -Path $binary -Destination $target -Force
    } catch {
        throw "could not replace $target. If deepcove is running, stop it and run this again."
    }
    return $target
}

# Add-ToUserPath offers to put the install folder on the user's PATH, which every new terminal reads, and
# puts it on this terminal's too, since the script runs inside it. With no one to ask, it changes nothing.
function Add-ToUserPath([string] $installDir) {
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (($userPath -split ';') -contains $installDir) {
        if (($env:Path -split ';') -notcontains $installDir) {
            $env:Path = "$installDir;$env:Path"
        }
        return 'ready'
    }
    if (-not [Environment]::UserInteractive -or [Console]::IsInputRedirected) {
        return 'missing'
    }
    $answer = Read-Host "Add $installDir to your PATH? [Y/n]"
    if ($answer -and $answer -notmatch '^[Yy]') {
        return 'missing'
    }
    $newPath = if ($userPath) { "$installDir;$userPath" } else { $installDir }
    [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
    $env:Path = "$installDir;$env:Path"
    return 'written'
}

# Start-DeepcoveService keeps deepcove running in the background, and starting again on its own.
function Start-DeepcoveService([string] $installDir) {
    if ($env:DEEPCOVE_NO_SERVICE -eq '1') {
        return $false
    }
    $exe = Join-Path $installDir 'deepcove.exe'
    # install starts a new service, but leaves one that was already running on the old binary. restart
    # puts this one in its place. What install prints, service status shows too.
    try {
        & $exe service install | Out-Null
        if ($LASTEXITCODE -ne 0) {
            return $false
        }
        & $exe service restart | Out-Host
        if ($LASTEXITCODE -ne 0) {
            return $false
        }
    } catch {
        return $false
    }
    Write-Host 'deepcove is running in the background, and starts on its own from now on.'
    return $true
}

function Write-NextSteps([string] $installDir, [string] $pathState, [bool] $running) {
    $overSsh = [bool] $env:SSH_CONNECTION
    Write-Host ''
    if (-not $running) {
        Write-Host 'Next, start it:'
        Write-Host '  deepcove start'
    } elseif ($overSsh) {
        Write-Host 'Next, sign in to Tailscale so your other machines can reach this one:'
        Write-Host '  deepcove network login'
    } else {
        Write-Host 'Next, open http://127.0.0.1:9000 in your browser.'
    }
    Write-Host ''
    Write-Host 'More commands:'
    if (-not $overSsh) {
        Write-Host '  deepcove dashboard         open it in your browser'
    }
    Write-Host '  deepcove service status    is it running, and where its logs are'
    Write-Host '  deepcove service restart   restart it'
    Write-Host '  deepcove help              everything else'
    # Once on PATH, this terminal has it too, so only a no leaves anything to do.
    if ($pathState -eq 'missing') {
        Write-Host ''
        Write-Host 'To use them, first run:'
        Write-Host "  `$env:Path = `"$installDir;`$env:Path`""
    }
}

Main
