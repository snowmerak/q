; Q lifecycle extension: Wails owns metadata, icons, architecture selection,
; companion files and uninstall registration through its generated helpers.
Unicode true
!define WAILS_INSTALL_SCOPE "user"
!define REQUEST_EXECUTION_LEVEL "user"
!include "wails_tools.nsh"
!include "MUI2.nsh"
!define BOOTSTRAP "MicrosoftEdgeWebview2Setup.exe"
!include "__Q_STUDIO_WEBVIEW2__"
VIProductVersion "${INFO_PRODUCTVERSION}.0"
VIAddVersionKey "ProductName" "${INFO_PRODUCTNAME}"
VIAddVersionKey "FileDescription" "${INFO_PRODUCTNAME} Installer"
VIAddVersionKey "ProductVersion" "${INFO_PRODUCTVERSION}"
VIAddVersionKey "CompanyName" "${INFO_COMPANYNAME}"
ManifestDPIAware true
!define MUI_ICON "..\icon.ico"
!define MUI_UNICON "..\icon.ico"
!define MUI_ABORTWARNING
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_RUN "$INSTDIR\${PRODUCT_EXECUTABLE}"
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"
Name "${INFO_PRODUCTNAME}"
OutFile "Q-Studio-installer.exe"
InstallDir "$LOCALAPPDATA\Programs\Q Studio"
ShowInstDetails show

Function .onInit
  !insertmacro wails.checkArchitecture
FunctionEnd

!macro WAIT_FOR_STUDIO PREFIX
Function ${PREFIX}WaitForStudio
  StrCpy $1 0
  ${Do}
    FindWindow $0 "QStudioApplication"
    ${If} $0 == 0
      Return
    ${EndIf}
    Sleep 250
    IntOp $1 $1 + 1
    ${If} $1 >= 80
      MessageBox MB_OK|MB_ICONSTOP "Q Studio is still stopping. Quit it from its tray menu and retry." /SD IDOK
      SetErrorLevel 1
      Abort
    ${EndIf}
  ${Loop}
FunctionEnd
!macroend
!insertmacro WAIT_FOR_STUDIO ""
!insertmacro WAIT_FOR_STUDIO "un."

Section "Q Studio"
  !insertmacro wails.setShellContext
  IfFileExists "$INSTDIR\${PRODUCT_EXECUTABLE}" 0 install
  ExecWait '"$INSTDIR\${PRODUCT_EXECUTABLE}" --quit'
  Call WaitForStudio
  install:
  Call EnsureWebView2
  SetOutPath "$INSTDIR"
  !insertmacro wails.files
  CreateShortcut "$SMPROGRAMS\Q Studio.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}"
  CreateShortcut "$DESKTOP\Q Studio.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}"
  !insertmacro wails.writeUninstaller
SectionEnd

Section "Uninstall"
  !insertmacro wails.setShellContext
  ExecWait '"$INSTDIR\${PRODUCT_EXECUTABLE}" --unregister-autostart'
  ExecWait '"$INSTDIR\${PRODUCT_EXECUTABLE}" --quit'
  Call un.WaitForStudio
  Delete "$INSTDIR\${PRODUCT_EXECUTABLE}"
  Delete "$INSTDIR\q-engine.exe"
  Delete "$INSTDIR\q.exe"
  Delete "$SMPROGRAMS\Q Studio.lnk"
  Delete "$DESKTOP\Q Studio.lnk"
  !insertmacro wails.deleteUninstaller
  RMDir "$INSTDIR"
  ; Preserve ~/.q, workspaces and user-created files.
SectionEnd
