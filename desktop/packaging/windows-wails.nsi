; Q v2 per-user Windows NSIS installer script (directly compilable with makensis).
Unicode true

!ifndef PRODUCT_NAME
  !define PRODUCT_NAME "Q"
!endif
!ifndef COMPANY_NAME
  !define COMPANY_NAME "snowmerak"
!endif
!ifndef PRODUCT_VERSION
  !define PRODUCT_VERSION "2.0.0"
!endif
!ifndef PRODUCT_EXECUTABLE
  !define PRODUCT_EXECUTABLE "q-desktop.exe"
!endif
!ifndef BINARY_PATH
  !define BINARY_PATH "..\..\bin\q-desktop.exe"
!endif
!ifndef BOOTSTRAP
  !define BOOTSTRAP "..\..\bin\nsis\MicrosoftEdgeWebview2Setup.exe"
!endif
!ifndef PRODUCT_ARCH
  !define PRODUCT_ARCH "amd64"
!endif
!ifndef OUT_FILE
  !define OUT_FILE "..\..\bin\Q-${PRODUCT_VERSION}-windows-${PRODUCT_ARCH}-setup.exe"
!endif

!define UNINST_REG_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\${PRODUCT_NAME}"

!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "x64.nsh"
!include "webview2.nsh"

Name "${PRODUCT_NAME}"
OutFile "${OUT_FILE}"
InstallDir "$LOCALAPPDATA\Programs\${PRODUCT_NAME}"
InstallDirRegKey HKCU "${UNINST_REG_KEY}" "InstallLocation"
RequestExecutionLevel user
ManifestDPIAware true
ShowInstDetails show

VIProductVersion "${PRODUCT_VERSION}.0"
VIAddVersionKey "ProductName" "${PRODUCT_NAME}"
VIAddVersionKey "FileDescription" "${PRODUCT_NAME} Installer"
VIAddVersionKey "ProductVersion" "${PRODUCT_VERSION}"
VIAddVersionKey "CompanyName" "${COMPANY_NAME}"

!define MUI_ICON "icon.ico"
!define MUI_UNICON "icon.ico"
!define MUI_ABORTWARNING

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_RUN "$INSTDIR\${PRODUCT_EXECUTABLE}"
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

!macro StopRunningApp
  DetailPrint "Checking for running ${PRODUCT_EXECUTABLE}..."
  nsExec::Exec '"$SYSDIR\taskkill.exe" /IM "${PRODUCT_EXECUTABLE}" /T'
  Pop $0
  ${If} $0 == 0
    DetailPrint "Closing running ${PRODUCT_EXECUTABLE}..."
    Sleep 1000
  ${EndIf}
  nsExec::Exec '"$SYSDIR\taskkill.exe" /F /IM "${PRODUCT_EXECUTABLE}" /T'
  Pop $0
  ${If} $0 == 0
    DetailPrint "Terminated running ${PRODUCT_EXECUTABLE}."
    Sleep 1000
  ${EndIf}
!macroend

Function .onInit
  ${If} ${RunningX64}
    SetRegView 64
  ${EndIf}
FunctionEnd

Section "Q"
  SetShellVarContext current
  Call EnsureWebView2
  !insertmacro StopRunningApp

  SetOutPath "$INSTDIR"
  File "/oname=${PRODUCT_EXECUTABLE}" "${BINARY_PATH}"

  CreateShortcut "$SMPROGRAMS\${PRODUCT_NAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}" "" "$INSTDIR\${PRODUCT_EXECUTABLE}" 0
  CreateShortcut "$DESKTOP\${PRODUCT_NAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}" "" "$INSTDIR\${PRODUCT_EXECUTABLE}" 0

  WriteUninstaller "$INSTDIR\Uninstall.exe"
  WriteRegStr HKCU "${UNINST_REG_KEY}" "DisplayName" "${PRODUCT_NAME}"
  WriteRegStr HKCU "${UNINST_REG_KEY}" "DisplayVersion" "${PRODUCT_VERSION}"
  WriteRegStr HKCU "${UNINST_REG_KEY}" "Publisher" "${COMPANY_NAME}"
  WriteRegStr HKCU "${UNINST_REG_KEY}" "DisplayIcon" "$INSTDIR\${PRODUCT_EXECUTABLE}"
  WriteRegStr HKCU "${UNINST_REG_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINST_REG_KEY}" "UninstallString" '"$INSTDIR\Uninstall.exe"'
  WriteRegDWORD HKCU "${UNINST_REG_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINST_REG_KEY}" "NoRepair" 1
SectionEnd

Section "Uninstall"
  SetShellVarContext current
  ${If} ${RunningX64}
    SetRegView 64
  ${EndIf}
  !insertmacro StopRunningApp

  ExecWait '"$INSTDIR\${PRODUCT_EXECUTABLE}" --unregister-autostart'
  Delete "$INSTDIR\${PRODUCT_EXECUTABLE}"
  Delete "$INSTDIR\Uninstall.exe"
  Delete "$SMPROGRAMS\${PRODUCT_NAME}.lnk"
  Delete "$DESKTOP\${PRODUCT_NAME}.lnk"
  DeleteRegKey HKCU "${UNINST_REG_KEY}"
  RMDir "$INSTDIR"
  ; Preserve ~/.q and user-created data directories.
SectionEnd
