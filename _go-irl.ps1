# Arguments for go-srtla, defined as an array of strings.
# This format ("-key", "value") is directly consumable by native executables.
$GO_SRTLA_ARGS = @(
    "-srtla_port", "5000",
    "-srt_hostname", "127.0.0.1",
    "-srt_port", "5001"
)

# Arguments for srt-live-reporter
$SRT_REPORTER_FROM = "srt://:5001?mode=listener"
# Uncomment the line below to use a passphrase
# $SRT_REPORTER_FROM = "srt://:5001?mode=listener&passphrase=1234567890"

$SRT_LIVE_REPORTER_ARGS = @(
    "-from", $SRT_REPORTER_FROM,
    "-to", "udp://127.0.0.1:5002",
    "-wsport", "8888"
)

# Arguments for obs-srt-bridge
$OBS_SRT_BRIDGE_ARGS = @(
    "-port", "9999"
)

# Path to each binary
$GO_SRTLA_BIN          = Join-Path -Path $PSScriptRoot -ChildPath "go-srtla.exe"
$SRT_LIVE_REPORTER_BIN = Join-Path -Path $PSScriptRoot -ChildPath "srt-live-reporter.exe"
$OBS_SRT_BRIDGE_BIN    = Join-Path -Path $PSScriptRoot -ChildPath "obs-srt-bridge.exe"

# =============================================================================
# Script Logic (Do not edit below this line unless you know what you are doing)
# =============================================================================
[Console]::OutputEncoding = [System.Text.Encoding]::GetEncoding('utf-8')
$jobs = @()
try {
    function StartAndMonitor {
        param(
            [string]$Name,
            [string]$Command,
            [string[]]$Arguments
        )

        while ($true) {
            if (-not (Test-Path -Path $Command -PathType Leaf)) {
                Write-Host "[Manager] ERROR: Binary for '$Name' not found at the expected path: '$Command'." -ForegroundColor Red
                throw "Required binary '$Command' not found."
            }

            Write-Host "[Manager] Starting '$Name' with arguments: $($Arguments -join ' ')" -ForegroundColor Cyan
            
            & $Command $Arguments *>&1 | ForEach-Object {
                $timestamp = Get-Date -Format 'yyyy-MM-dd HH:mm:ss'
                Write-Host "[$Name`:$timestamp] $_"
            }
            
            $exitCode = $LASTEXITCODE

            Write-Host "[Manager] Process '$Name' exited with code $exitCode. Restarting in 5 seconds..." -ForegroundColor Yellow
            Start-Sleep -Seconds 5
        }
    }

    # ASCII Art
    @"


      ██████╗   ██████╗         ██╗ ██████╗  ██╗     
     ██╔════╝  ██╔═══██╗        ██║ ██╔══██╗ ██║     
     ██║  ███╗ ██║   ██║ █████╗ ██║ ██████╔╝ ██║     
     ██║   ██║ ██║   ██║ ╚════╝ ██║ ██╔══██╗ ██║     
     ╚██████╔╝ ╚██████╔╝        ██║ ██║  ██║ ███████╗
      ╚═════╝   ╚═════╝         ╚═╝ ╚═╝  ╚═╝ ╚══════╝

"@

    $jobs += Start-Job -ScriptBlock { 
        ${function:StartAndMonitor} = $using:function:StartAndMonitor
        StartAndMonitor -Name "go-srtla" -Command $using:GO_SRTLA_BIN -Arguments $using:GO_SRTLA_ARGS
    }

    $jobs += Start-Job -ScriptBlock {
        ${function:StartAndMonitor} = $using:function:StartAndMonitor
        StartAndMonitor -Name "srt-live-reporter" -Command $using:SRT_LIVE_REPORTER_BIN -Arguments $using:SRT_LIVE_REPORTER_ARGS
    }

    $jobs += Start-Job -ScriptBlock {
        ${function:StartAndMonitor} = $using:function:StartAndMonitor
        StartAndMonitor -Name "obs-srt-bridge" -Command $using:OBS_SRT_BRIDGE_BIN -Arguments $using:OBS_SRT_BRIDGE_ARGS
    }


    while ($true) {
        $jobs | ForEach-Object {
            $output = Receive-Job -Job $_
            if ($output) {
                $output | ForEach-Object { Write-Host $_ }
            }
        }
        Start-Sleep -Milliseconds 200
    }
}
finally {
    Get-Job | Stop-Job -Force
    Get-Job | Remove-Job -Force
    Write-Host "[Manager] All processes stopped. Exiting." -ForegroundColor Green
}
