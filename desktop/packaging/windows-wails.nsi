; Q v2 per-user NSIS installer template for Wails v3 packaging.
Unicode true
!define WAILS_INSTALL_SCOPE "user"
!define REQUEST_EXECUTION_LEVEL "user"
!include "wails_tools.nsh"
!include "MUI2.nsh"
!define BOOTSTRAP "MicrosoftEdgeWebview2Setup.exe"
!include "webview2.nsh"
VIProductVersion "${INFO_PRODUCTVERSION}.0"
VIAddVersionKey "ProductName" "${INFO_PRODUCTNAME}"
VIAddVersionKey "FileDescription" "${INFO_PRODUCTNAME} Installer"
VIAddVersionKey "ProductVersion" "${INFO_PRODUCTVERSION}"
VIAddVersionKey "CompanyName" "${INFO_COMPANYNAME}"
ManifestDPIAware true
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
Name "${INFO_PRODUCTNAME}"
OutFile "Q-Setup.exe"
InstallDir "$LOCALAPPDATA\Programs\Q"
ShowInstDetails show

Function .onInit
  !insertmacro wails.checkArchitecture
FunctionEnd

Section "Q"
  !insertmacro wails.setShellContext
  Call EnsureWebView2
  SetOutPath "$INSTDIR"
  !insertmacro wails.files
  CreateShortcut "$SMPROGRAMS\Q.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}"
  CreateShortcut "$DESKTOP\Q.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}"
  !insertmacro wails.writeUninstaller
SectionEnd

Section "Uninstall"
  !insertmacro wails.setShellContext
  ExecWait '"$INSTDIR\${PRODUCT_EXECUTABLE}" --unregister-autostart'
  Delete "$INSTDIR\${PRODUCT_EXECUTABLE}"
  Delete "$SMPROGRAMS\Q.lnk"
  Delete "$DESKTOP\Q.lnk"
  !insertmacro wails.deleteUninstaller
  RMDir "$INSTDIR"
  ; Preserve ~/.q and user-created data directories.
SectionEnd
