param(
    [string]$ResultsRoot = ".structured-output-results"
)

$ErrorActionPreference = "Stop"
$repo = Split-Path -Parent $PSScriptRoot
$schemaPath = Join-Path $repo "testdata/review-output.schema.json"
$schema = Get-Content -Raw -LiteralPath $schemaPath
$stamp = Get-Date -Format "yyyyMMdd-HHmmss"
$results = Join-Path $repo (Join-Path $ResultsRoot $stamp)
New-Item -ItemType Directory -Path $results -Force | Out-Null

$prompt = @"
Do not modify files. Review only session.go and provider_cli.go.
Return at most two material issues with concrete file evidence.
Set done=true only if no material issue exists.
"@

function Assert-ReviewOutput {
    param([string]$Provider, [object]$Value)

    if ($null -eq $Value.done -or [string]::IsNullOrWhiteSpace($Value.summary) -or
        $null -eq $Value.issues -or $null -eq $Value.plan) {
        throw "$Provider output is missing a required top-level field"
    }
    foreach ($issue in $Value.issues) {
        if ([string]::IsNullOrWhiteSpace($issue.id) -or
            $issue.severity -notin @("low", "medium", "high") -or
            [string]::IsNullOrWhiteSpace($issue.description) -or
            [string]::IsNullOrWhiteSpace($issue.evidence) -or
            [string]::IsNullOrWhiteSpace($issue.acceptance_criteria)) {
            throw "$Provider returned an invalid issue"
        }
    }
    if ($Value.done -and $Value.issues.Count -ne 0) {
        throw "$Provider returned done=true with unresolved issues"
    }
    $issueIds = @($Value.issues | ForEach-Object { $_.id })
    foreach ($item in $Value.plan) {
        if ($item.issue_id -notin $issueIds -or [string]::IsNullOrWhiteSpace($item.action)) {
            throw "$Provider returned a plan without a matching issue"
        }
    }
    $Value | ConvertTo-Json -Depth 20 |
        Set-Content -LiteralPath (Join-Path $results "$Provider-normalized.json") -Encoding utf8
    Write-Host "$Provider structured output: PASS"
}

Push-Location $repo
try {
    codex exec --sandbox read-only --ephemeral --output-schema $schemaPath `
        --output-last-message (Join-Path $results "codex.json") --color never $prompt

    grok --cwd $repo --permission-mode plan --output-format json `
        --json-schema $schema --single $prompt |
        Set-Content -LiteralPath (Join-Path $results "grok-wrapper.json") -Encoding utf8

    claude --permission-mode plan --output-format json --json-schema $schema `
        --print $prompt |
        Set-Content -LiteralPath (Join-Path $results "claude-wrapper.json") -Encoding utf8

    $agyPrompt = "$prompt Return ONLY one JSON object without markdown fences matching this schema: $schema"
    go run . agy $agyPrompt |
        Set-Content -LiteralPath (Join-Path $results "agy.txt") -Encoding utf8

    $codex = Get-Content -Raw -LiteralPath (Join-Path $results "codex.json") | ConvertFrom-Json
    $grokWrapper = Get-Content -Raw -LiteralPath (Join-Path $results "grok-wrapper.json") | ConvertFrom-Json
    $claudeWrapper = Get-Content -Raw -LiteralPath (Join-Path $results "claude-wrapper.json") | ConvertFrom-Json
    $agy = Get-Content -Raw -LiteralPath (Join-Path $results "agy.txt") | ConvertFrom-Json

    Assert-ReviewOutput "codex" $codex
    Assert-ReviewOutput "grok" $grokWrapper.structuredOutput
    Assert-ReviewOutput "claude" $claudeWrapper.structured_output
    Assert-ReviewOutput "agy" $agy

    Write-Host "Structured-output results retained at: $results"
} finally {
    Pop-Location
}
