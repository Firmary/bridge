; Firmary Link installer for Windows (per user, no admin).
;
; Built on Linux with NSIS 3:
;   makensis -DVERSION=0.2.0 -DX64_EXE="$PWD/dist/firmary-link-windows-x64.exe" \
;     -DARM64_EXE="$PWD/dist/firmary-link-windows-arm64.exe" -DOUTFILE="$PWD/dist/Firmary-Link-Setup.exe" \
;     packaging/windows/installer.nsi
;
; It carries both executables and installs the one that matches the machine's native architecture.

Unicode true
SetCompressor /SOLID lzma

!ifndef VERSION
  !error "pass -DVERSION=X.Y.Z"
!endif
!ifndef X64_EXE
  !error "pass -DX64_EXE=path/to/firmary-link-windows-x64.exe"
!endif
!ifndef ARM64_EXE
  !error "pass -DARM64_EXE=path/to/firmary-link-windows-arm64.exe"
!endif
!ifndef OUTFILE
  !define OUTFILE "Firmary-Link-Setup.exe"
!endif

!define APP_NAME "Firmary Link"
!define APP_EXE "firmary-link.exe"
!define PUBLISHER "Firmary"
; makensis runs from this script's folder, so this path is relative to it; pass the exe paths as absolute paths.
!define ICON "../../assets/windows/firmary-link.ico"
; The app's own Open-at-login toggle reads and writes this exact value name.
!define RUN_KEY "Software\Microsoft\Windows\CurrentVersion\Run"
!define RUN_VALUE "Firmary Link"
!define STARTUP_APPROVED_KEY "Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run"
!define UNINSTALL_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\FirmaryLink"
!define TASK_NAME "Firmary Link"

!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "x64.nsh"

Name "${APP_NAME}"
OutFile "${OUTFILE}"
RequestExecutionLevel user
InstallDir "$LOCALAPPDATA\Programs\${APP_NAME}"
BrandingText "${APP_NAME} ${VERSION}"
ShowInstDetails nevershow
ShowUninstDetails nevershow

VIProductVersion "${VERSION}.0"
VIAddVersionKey "ProductName" "${APP_NAME}"
VIAddVersionKey "CompanyName" "${PUBLISHER}"
VIAddVersionKey "FileDescription" "${APP_NAME} Setup"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "LegalCopyright" "Copyright 2026 Firmary"

!define MUI_ICON "${ICON}"
!define MUI_UNICON "${ICON}"
!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "$INSTDIR\${APP_EXE}"
!define MUI_FINISHPAGE_RUN_TEXT "Open ${APP_NAME} now"

!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

Function .onInit
  SetShellVarContext current
  ${IfNot} ${IsNativeAMD64}
  ${AndIfNot} ${IsNativeARM64}
    MessageBox MB_ICONSTOP|MB_OK "${APP_NAME} needs 64-bit Windows (x64 or Arm64)." /SD IDOK
    Abort
  ${EndIf}
FunctionEnd

Function un.onInit
  SetShellVarContext current
FunctionEnd

Section "Install"
  ; An older copy may be running from the tray; stop it so its file can be replaced.
  nsExec::Exec '"$SYSDIR\taskkill.exe" /F /IM ${APP_EXE}'
  Pop $0
  Sleep 500

  SetOutPath "$INSTDIR"
  ${If} ${IsNativeARM64}
    File "/oname=${APP_EXE}" "${ARM64_EXE}"
  ${Else}
    File "/oname=${APP_EXE}" "${X64_EXE}"
  ${EndIf}
  File "/oname=firmary-link.ico" "${ICON}"
  WriteUninstaller "$INSTDIR\uninstall.exe"

  CreateShortCut "$SMPROGRAMS\${APP_NAME}.lnk" "$INSTDIR\${APP_EXE}" "" "$INSTDIR\firmary-link.ico" 0

  ; Open at login is on by default; the app's own toggle turns it off.
  WriteRegStr HKCU "${RUN_KEY}" "${RUN_VALUE}" '"$INSTDIR\${APP_EXE}"'

  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayName" "${APP_NAME}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "Publisher" "${PUBLISHER}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayIcon" "$INSTDIR\firmary-link.ico"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "URLInfoAbout" "https://github.com/Firmary/bridge"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegStr HKCU "${UNINSTALL_KEY}" "QuietUninstallString" '"$INSTDIR\uninstall.exe" /S'
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "NoRepair" 1
SectionEnd

Section "Uninstall"
  nsExec::Exec '"$SYSDIR\taskkill.exe" /F /IM ${APP_EXE}'
  Pop $0
  nsExec::Exec '"$SYSDIR\schtasks.exe" /Delete /TN "${TASK_NAME}" /F'
  Pop $0
  Sleep 500

  DeleteRegValue HKCU "${RUN_KEY}" "${RUN_VALUE}"
  DeleteRegValue HKCU "${STARTUP_APPROVED_KEY}" "${RUN_VALUE}"
  Delete "$SMPROGRAMS\${APP_NAME}.lnk"

  ; The install folder is the app's own (there is no folder choice), so anything the updater left there goes too.
  RMDir /r "$INSTDIR"

  ; The app's data: device key, pairing, sign-ins, logs.
  RMDir /r "$LOCALAPPDATA\Firmary\Link"
  RMDir "$LOCALAPPDATA\Firmary"

  DeleteRegKey HKCU "${UNINSTALL_KEY}"
SectionEnd
