# Signs one Windows binary for the desktop release. scripts/bundle.sh runs it
# on the bundled baum.exe and, when BAUM_WINDOWS_SIGN=1, makes it the Tauri
# bundler's bundle.windows.signCommand, which signs the app, the installer and
# the uninstaller. Configure exactly one method:
#
#   Azure Trusted Signing (needs trusted-signing-cli on PATH):
#     AZURE_TENANT_ID, AZURE_CLIENT_ID, AZURE_CLIENT_SECRET,
#     AZURE_TRUSTED_SIGNING_ENDPOINT, AZURE_TRUSTED_SIGNING_ACCOUNT,
#     AZURE_TRUSTED_SIGNING_PROFILE
#   A code signing certificate:
#     WINDOWS_CERTIFICATE_PATH (a .pfx file), WINDOWS_CERTIFICATE_PASSWORD
param(
    [Parameter(Mandatory = $true)]
    [string]$Path
)
$ErrorActionPreference = 'Stop'

if ($env:AZURE_TRUSTED_SIGNING_ACCOUNT) {
    & trusted-signing-cli `
        -e $env:AZURE_TRUSTED_SIGNING_ENDPOINT `
        -a $env:AZURE_TRUSTED_SIGNING_ACCOUNT `
        -c $env:AZURE_TRUSTED_SIGNING_PROFILE `
        -d baum `
        $Path
}
elseif ($env:WINDOWS_CERTIFICATE_PATH) {
    $signtool = Get-ChildItem "${env:ProgramFiles(x86)}\Windows Kits\10\bin\*\x64\signtool.exe" |
        Sort-Object { [version]$_.Directory.Parent.Name } -Descending |
        Select-Object -First 1
    if (-not $signtool) { throw 'signtool.exe was not found in the Windows SDK' }
    & $signtool.FullName sign /fd sha256 /td sha256 /tr http://timestamp.digicert.com `
        /d baum /f $env:WINDOWS_CERTIFICATE_PATH /p $env:WINDOWS_CERTIFICATE_PASSWORD $Path
}
else {
    throw 'BAUM_WINDOWS_SIGN is set but no Windows signing method is configured'
}
if ($LASTEXITCODE -ne 0) { throw "signing $Path failed ($LASTEXITCODE)" }
