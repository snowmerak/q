!ifndef Q_STUDIO_WEBVIEW2_NSH
!define Q_STUDIO_WEBVIEW2_NSH
!include "LogicLib.nsh"

; Microsoft's documented Evergreen Runtime detection key. Both per-user and
; per-machine registrations must be checked, including the 32-bit view on ARM64.
!ifndef WEBVIEW_CLIENT_KEY
!define WEBVIEW_CLIENT_KEY "Software\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"
!endif

Var WebViewVersion
Var WebViewSetupExit
Var WebViewSetupHex

!macro READ_WEBVIEW ROOT
  ReadRegStr $WebViewVersion ${ROOT} "${WEBVIEW_CLIENT_KEY}" "pv"
  ${If} $WebViewVersion != ""
  ${AndIf} $WebViewVersion != "0.0.0.0"
    SetRegView lastused
    Return
  ${EndIf}
!macroend

Function DetectWebView2
  StrCpy $WebViewVersion ""
  SetRegView 32
  !insertmacro READ_WEBVIEW HKLM
  !insertmacro READ_WEBVIEW HKCU
  SetRegView 64
  !insertmacro READ_WEBVIEW HKLM
  !insertmacro READ_WEBVIEW HKCU
  SetRegView 32
  StrCpy $WebViewVersion ""
FunctionEnd

Function EnsureWebView2
  Call DetectWebView2
  ${If} $WebViewVersion != ""
    DetailPrint "Using installed WebView2 Runtime $WebViewVersion"
    Return
  ${EndIf}

  InitPluginsDir
  SetOutPath "$PLUGINSDIR"
  File /oname=WebView2Setup.exe "${BOOTSTRAP}"
  ClearErrors
  ExecWait '"$PLUGINSDIR\WebView2Setup.exe" /silent /install' $WebViewSetupExit
  ${If} ${Errors}
    MessageBox MB_OK|MB_ICONSTOP "Could not start the WebView2 installer. Install Microsoft Edge WebView2 Runtime and retry Q Studio setup." /SD IDOK
    SetErrorLevel 1
    Abort
  ${EndIf}

  ; A nonzero bootstrapper result can mean already installed. Availability is
  ; authoritative: never fail a usable runtime or accept a missing one just
  ; because the bootstrapper returned zero.
  Call DetectWebView2
  ${If} $WebViewVersion != ""
    DetailPrint "WebView2 Runtime available: $WebViewVersion (setup exit $WebViewSetupExit)"
    Return
  ${EndIf}
  IntFmt $WebViewSetupHex "0x%08X" $WebViewSetupExit
  MessageBox MB_OK|MB_ICONSTOP "WebView2 Runtime is not available after setup (exit $WebViewSetupExit / $WebViewSetupHex). Check Microsoft Edge Update installation logs or install WebView2 Runtime and retry. If setup requested a restart, restart Windows first." /SD IDOK
  SetErrorLevel 1
  Abort
FunctionEnd
!endif
