# Signs the Windows installer that scripts/bundle.sh built unsigned, with
# Microsoft's signtool only:
#
#   pwsh -File scripts/windows-sign.ps1 -Path baum-desktop-windows-amd64-setup.exe
#
# The release runs it in a job that holds the signing secrets and compiles
# nothing. The app and the bundled baum.exe inside the installer, and the
# uninstaller it writes, stay unsigned: signing them would mean running the
# bundler with the secrets in its environment (see desktop/README.md).
# Configure exactly one method:
#
#   A code signing certificate:
#     WINDOWS_CERTIFICATE_PATH (a .pfx file), WINDOWS_CERTIFICATE_PASSWORD.
#     The certificate is imported into the current user's store for the
#     signature and removed afterwards; the password never appears on a
#     command line.
#   Azure Artifact Signing (formerly Trusted Signing):
#     AZURE_TENANT_ID, AZURE_CLIENT_ID, AZURE_CLIENT_SECRET,
#     AZURE_TRUSTED_SIGNING_ENDPOINT, AZURE_TRUSTED_SIGNING_ACCOUNT,
#     AZURE_TRUSTED_SIGNING_PROFILE. Uses Microsoft's signtool plug-in from
#     the Microsoft.ArtifactSigning.Client package, pinned by SHA-256.
param(
    [Parameter(Mandatory = $true)]
    [string]$Path
)
$ErrorActionPreference = 'Stop'

$Path = (Resolve-Path -LiteralPath $Path).Path
$timestamp = 'http://timestamp.acs.microsoft.com'
$signtool = Get-ChildItem "${env:ProgramFiles(x86)}\Windows Kits\10\bin\*\x64\signtool.exe" |
    Sort-Object { [version]$_.Directory.Parent.Name } -Descending |
    Select-Object -First 1
if (-not $signtool) { throw 'signtool.exe was not found in the Windows SDK' }

if ($env:AZURE_TRUSTED_SIGNING_ACCOUNT) {
    $version = '1.0.128'
    $sha256 = '74bd7d27e6ce1051409c38d9b46bc8df0400ecd643d51ffbf2ac00869061e40b'
    $work = Join-Path ([System.IO.Path]::GetTempPath()) ([System.Guid]::NewGuid())
    New-Item -ItemType Directory -Path $work | Out-Null
    try {
        $package = Join-Path $work 'client.zip' # Expand-Archive takes only .zip
        Invoke-WebRequest -UseBasicParsing -OutFile $package `
            "https://api.nuget.org/v3-flatcontainer/microsoft.artifactsigning.client/$version/microsoft.artifactsigning.client.$version.nupkg"
        if ((Get-FileHash -Algorithm SHA256 -LiteralPath $package).Hash -ne $sha256) {
            throw 'Microsoft.ArtifactSigning.Client does not have the pinned SHA-256'
        }
        Expand-Archive -LiteralPath $package -DestinationPath (Join-Path $work 'client')
        $metadata = Join-Path $work 'metadata.json'
        @{
            Endpoint               = $env:AZURE_TRUSTED_SIGNING_ENDPOINT
            CodeSigningAccountName = $env:AZURE_TRUSTED_SIGNING_ACCOUNT
            CertificateProfileName = $env:AZURE_TRUSTED_SIGNING_PROFILE
        } | ConvertTo-Json | Set-Content -LiteralPath $metadata -Encoding utf8
        $dlib = Join-Path $work 'client\bin\x64\Azure.CodeSigning.Dlib.dll'
        & $signtool.FullName sign /v /fd SHA256 /tr $timestamp /td SHA256 /d baum `
            /dlib $dlib /dmdf $metadata $Path
        if ($LASTEXITCODE -ne 0) { throw "signtool failed ($LASTEXITCODE)" }
    }
    finally {
        Remove-Item -Recurse -Force -LiteralPath $work -ErrorAction SilentlyContinue
    }
}
elseif ($env:WINDOWS_CERTIFICATE_PATH) {
    $password = ConvertTo-SecureString -String $env:WINDOWS_CERTIFICATE_PASSWORD -AsPlainText -Force
    $certificate = Import-PfxCertificate -FilePath $env:WINDOWS_CERTIFICATE_PATH `
        -CertStoreLocation Cert:\CurrentUser\My -Password $password
    try {
        & $signtool.FullName sign /fd SHA256 /tr $timestamp /td SHA256 /d baum `
            /s My /sha1 $certificate.Thumbprint $Path
        if ($LASTEXITCODE -ne 0) { throw "signtool failed ($LASTEXITCODE)" }
    }
    finally {
        Remove-Item -LiteralPath "Cert:\CurrentUser\My\$($certificate.Thumbprint)" -DeleteKey -ErrorAction SilentlyContinue
    }
}
else {
    throw 'No Windows signing method is configured'
}

$signature = Get-AuthenticodeSignature -LiteralPath $Path
if ($signature.Status -ne 'Valid') { throw "the signature of $Path is $($signature.Status)" }
Write-Output "signed $Path ($($signature.SignerCertificate.Subject))"
