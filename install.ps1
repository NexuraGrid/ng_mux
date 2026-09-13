# ngmux installer for Windows — downloads the latest prebuilt binary from
# GitHub Releases and puts it on your PATH.
#
#   irm https://raw.githubusercontent.com/NexuraGrid/ng_mux/main/install.ps1 | iex
#
# Run elevated (Administrator) to install for every user under
# %ProgramFiles%\ngmux and the system PATH; otherwise it installs just for you
# under %LOCALAPPDATA%\Programs\ngmux and your user PATH.
#
# Environment overrides:
#   $env:NGMUX_INSTALL_DIR   where to put ngmux.exe
#   $env:NGMUX_VERSION       tag to install          (default: latest release)

$ErrorActionPreference = 'Stop'

$repo = 'NexuraGrid/ng_mux'

$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()
	).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
$pathScope = if ($isAdmin) { 'Machine' } else { 'User' }

if ($env:NGMUX_INSTALL_DIR) {
	$installDir = $env:NGMUX_INSTALL_DIR
} elseif ($isAdmin) {
	$installDir = Join-Path $env:ProgramFiles 'ngmux'
} else {
	$installDir = Join-Path $env:LOCALAPPDATA 'Programs\ngmux'
}

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
	'AMD64' { 'amd64' }
	'ARM64' { 'arm64' }
	default { throw "unsupported architecture: $($env:PROCESSOR_ARCHITECTURE)" }
}

$tag = $env:NGMUX_VERSION
if (-not $tag) {
	$rel = Invoke-RestMethod "https://api.github.com/repos/$repo/releases/latest"
	$tag = $rel.tag_name
}
if (-not $tag) { throw "could not determine the latest release (is one published yet?)" }

$asset = "ngmux_windows_${arch}.exe"
$url   = "https://github.com/$repo/releases/download/$tag/$asset"

Write-Host "ngmux $tag  (windows/$arch)"
Write-Host "  -> $installDir\ngmux.exe"

New-Item -ItemType Directory -Force -Path $installDir | Out-Null
$dest = Join-Path $installDir 'ngmux.exe'
$tmp  = "$dest.new"
Invoke-WebRequest -Uri $url -OutFile $tmp

# Best-effort checksum check.
try {
	$sums = (Invoke-WebRequest -Uri "https://github.com/$repo/releases/download/$tag/SHA256SUMS" -UseBasicParsing).Content
	$want = ($sums -split "`n" | ForEach-Object { $_ -replace '\*','' } |
		Where-Object { $_ -match "\s$([regex]::Escape($asset))$" } |
		ForEach-Object { ($_ -split '\s+')[0] })
	if ($want) {
		$got = (Get-FileHash -Algorithm SHA256 $tmp).Hash.ToLower()
		if ($got -ne $want.ToLower()) { throw "checksum mismatch for $asset" }
	}
} catch { if ($_.Exception.Message -like '*checksum mismatch*') { throw } }

Move-Item -Force $tmp $dest

# --- Put the install dir on PATH (system scope when elevated, else user) ---
# Idempotent and tolerant of casing / a trailing backslash.
function Test-DirOnPath([string]$dir, [string]$scope) {
	$cur = [Environment]::GetEnvironmentVariable('Path', $scope)
	if (-not $cur) { return $false }
	$want = $dir.TrimEnd('\').ToLowerInvariant()
	foreach ($e in ($cur -split ';')) {
		if ($e -and $e.TrimEnd('\').ToLowerInvariant() -eq $want) { return $true }
	}
	return $false
}

function Remove-DirFromPath([string]$dir, [string]$scope) {
	$cur = [Environment]::GetEnvironmentVariable('Path', $scope)
	if (-not $cur) { return }
	$want = $dir.TrimEnd('\').ToLowerInvariant()
	$kept = @($cur -split ';' | Where-Object { $_ -and $_.TrimEnd('\').ToLowerInvariant() -ne $want })
	$next = ($kept -join ';')
	if ($next -ne $cur) { [Environment]::SetEnvironmentVariable('Path', $next, $scope) }
}

$scopeLabel = if ($pathScope -eq 'Machine') { 'system' } else { 'user' }
if (-not (Test-DirOnPath $installDir $pathScope)) {
	$cur = [Environment]::GetEnvironmentVariable('Path', $pathScope)
	$new = if ($cur) { $cur.TrimEnd(';') + ';' + $installDir } else { $installDir }
	[Environment]::SetEnvironmentVariable('Path', $new, $pathScope)
	Write-Host ""
	Write-Host "Added $installDir to the $scopeLabel PATH."
} else {
	Write-Host ""
	Write-Host "$installDir is already on the $scopeLabel PATH."
}

# A system install supersedes an earlier per-user one at the default location:
# drop that stale user PATH entry so an old copy can't shadow this one.
if ($pathScope -eq 'Machine') {
	$oldUserDir = Join-Path $env:LOCALAPPDATA 'Programs\ngmux'
	if ($oldUserDir -ne $installDir -and (Test-DirOnPath $oldUserDir 'User')) {
		Remove-DirFromPath $oldUserDir 'User'
		Write-Host "Removed the old per-user entry $oldUserDir from your user PATH."
	}
}

# Make it usable in this session immediately.
if (($env:Path -split ';' | ForEach-Object { $_.TrimEnd('\').ToLowerInvariant() }) -notcontains $installDir.TrimEnd('\').ToLowerInvariant()) {
	$env:Path = "$env:Path;$installDir"
}

# Broadcast the environment change so processes started afterwards (new
# terminals, launched from Explorer/Start) pick it up without a sign-out.
try {
	if (-not ('Win32.Env' -as [type])) {
		Add-Type -Namespace Win32 -Name Env -MemberDefinition @'
[System.Runtime.InteropServices.DllImport("user32.dll", SetLastError=true, CharSet=System.Runtime.InteropServices.CharSet.Auto)]
public static extern System.IntPtr SendMessageTimeout(System.IntPtr hWnd, uint Msg, System.UIntPtr wParam, string lParam, uint fuFlags, uint uTimeout, out System.UIntPtr lpdwResult);
'@
	}
	$res = [System.UIntPtr]::Zero
	[void][Win32.Env]::SendMessageTimeout([System.IntPtr]0xffff, 0x1A, [System.UIntPtr]::Zero, 'Environment', 2, 5000, [ref]$res)
} catch { }

# --- Tell the user plainly where things stand ---
Write-Host ""
$profileHijacksPath = $false
foreach ($pf in @($PROFILE.CurrentUserAllHosts, $PROFILE.CurrentUserCurrentHost)) {
	if ($pf -and (Test-Path $pf) -and (Select-String -Path $pf -Pattern '\$env:Path\s*=[^=]' -Quiet -ErrorAction SilentlyContinue)) {
		$profileHijacksPath = $true
	}
}
Write-Host "Done. Open a NEW terminal window and run:  ngmux"
Write-Host "(works in this session already)"
if (-not $isAdmin) {
	Write-Host ""
	Write-Host "Tip: re-run this elevated (Administrator) to install for all users"
	Write-Host "     under Program Files and the system PATH instead."
}
if ($profileHijacksPath) {
	Write-Host ""
	Write-Host "Note: your PowerShell profile sets `$env:Path directly, which can hide ngmux"
	Write-Host "even once it is on PATH. Change that line to append (`$env:Path += ...),"
	Write-Host "or add a function once:"
	Write-Host "  'function ngmux { & `"$dest`" @args }' | Add-Content `$PROFILE"
}
Write-Host ""
Write-Host "Full path, always works:  $dest"
