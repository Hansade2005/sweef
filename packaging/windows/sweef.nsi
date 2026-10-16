; Sweef installer (NSIS 3, Unicode). Primary Windows installer.
;
; Build (works on Linux, macOS and Windows with makensis):
;   makensis -DVERSION=0.1.0-alpha.7 -DVERSION4=0.1.0.7 -DARCH=amd64 \
;            -DSRCDIR=dist/bin/windows_amd64 -DICON=dist/icons/sweef.ico \
;            -DNOTICE=NOTICE -DOUTFILE=dist/sweef_..._setup.exe packaging/windows/sweef.nsi
; or simply packaging/windows/build-installer.sh
;
; Install modes (MultiUser): per-user is the default and needs no administrator rights
; (installs to %LOCALAPPDATA%\Programs\Sweef, registers under HKCU). "All users" installs
; to Program Files and registers under HKLM after a UAC prompt.
; Silent: Setup.exe /S [/currentuser | /allusers] [/D=C:\path]   (/D must be last)

Unicode true
SetCompressor /SOLID lzma
ManifestDPIAware true
ManifestLongPathAware true
ManifestSupportedOS Win10

!ifndef VERSION
  !error "pass -DVERSION=..."
!endif
!ifndef VERSION4
  !error "pass -DVERSION4=a.b.c.d"
!endif
!ifndef ARCH
  !define ARCH "amd64"
!endif

!define APPNAME     "Sweef"
!define COMPANY     "Sweef"
!define EXE_CLI     "sweef.exe"
!define EXE_GUI     "SweefLauncher.exe"
!define UNINST_KEY  "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APPNAME}"
!define CLIENT_KEY  "Software\Clients\StartMenuInternet\${APPNAME}"
!define CAPS_KEY    "${CLIENT_KEY}\Capabilities"
!define PROGID_HTML "SweefHTML"
!define PROGID_URL  "SweefURL"

Name "${APPNAME} ${VERSION}"
OutFile "${OUTFILE}"
BrandingText "${APPNAME} ${VERSION} (alpha)"
Icon "${ICON}"
UninstallIcon "${ICON}"

VIProductVersion "${VERSION4}"
VIAddVersionKey "ProductName"     "${APPNAME}"
VIAddVersionKey "CompanyName"     "${COMPANY}"
VIAddVersionKey "FileDescription" "${APPNAME} Browser installer"
VIAddVersionKey "FileVersion"     "${VERSION}"
VIAddVersionKey "ProductVersion"  "${VERSION}"
VIAddVersionKey "LegalCopyright"  "${COMPANY}"

; ---- multi-user: per-user default, optional all-users --------------------------
!define MULTIUSER_EXECUTIONLEVEL Highest
!define MULTIUSER_MUI
!define MULTIUSER_INSTALLMODE_COMMANDLINE
!define MULTIUSER_INSTALLMODE_DEFAULT_CURRENTUSER
!define MULTIUSER_INSTALLMODE_ALLOW_ELEVATION
!define MULTIUSER_USE_PROGRAMFILES64
!define MULTIUSER_INSTALLMODE_INSTDIR "${APPNAME}"
!define MULTIUSER_INSTALLMODE_INSTDIR_REGISTRY_KEY "${UNINST_KEY}"
!define MULTIUSER_INSTALLMODE_INSTDIR_REGISTRY_VALUENAME "InstallLocation"
!define MULTIUSER_INSTALLMODE_DEFAULT_REGISTRY_KEY "${UNINST_KEY}"
!define MULTIUSER_INSTALLMODE_DEFAULT_REGISTRY_VALUENAME "InstallLocation"

!include MUI2.nsh
!include MultiUser.nsh
!include LogicLib.nsh
!include WinVer.nsh
!include x64.nsh

; ---- pages --------------------------------------------------------------------
!define MUI_ICON "${ICON}"
!define MUI_UNICON "${ICON}"
!define MUI_ABORTWARNING
!insertmacro MUI_PAGE_WELCOME
!insertmacro MULTIUSER_PAGE_INSTALLMODE
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_COMPONENTS
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_RUN
!define MUI_FINISHPAGE_RUN_TEXT "Start ${APPNAME}"
!define MUI_FINISHPAGE_RUN_FUNCTION LaunchSweef
!define MUI_FINISHPAGE_SHOWREADME
!define MUI_FINISHPAGE_SHOWREADME_TEXT "Choose ${APPNAME} as my default browser (opens Windows Settings)"
!define MUI_FINISHPAGE_SHOWREADME_FUNCTION OpenDefaultApps
!define MUI_FINISHPAGE_NOREBOOTSUPPORT
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

; ---- init ---------------------------------------------------------------------
Function .onInit
  ${IfNot} ${AtLeastWin10}
    MessageBox MB_OK|MB_ICONSTOP "${APPNAME} requires Windows 10 or later."
    Abort
  ${EndIf}
  ${IfNot} ${RunningX64}
    MessageBox MB_OK|MB_ICONSTOP "${APPNAME} requires a 64-bit version of Windows."
    Abort
  ${EndIf}
  !insertmacro MULTIUSER_INIT
FunctionEnd

Function un.onInit
  !insertmacro MULTIUSER_UNINIT
FunctionEnd

Function LaunchSweef
  ; run unelevated even if the installer was elevated for an all-users install
  Exec '"$WINDIR\explorer.exe" "$INSTDIR\${EXE_GUI}"'
FunctionEnd

Function OpenDefaultApps
  ExecShell "open" "ms-settings:defaultapps"
FunctionEnd

; ---- sections -----------------------------------------------------------------
Section "${APPNAME} (required)" SecMain
  SectionIn RO
  SetRegView 64
  SetOutPath "$INSTDIR"
  File "${SRCDIR}\${EXE_CLI}"
  File "${SRCDIR}\${EXE_GUI}"
  File "${NOTICE}"
  WriteUninstaller "$INSTDIR\uninstall.exe"

  ; Start menu
  CreateShortcut "$SMPROGRAMS\${APPNAME}.lnk" "$INSTDIR\${EXE_GUI}" "" "$INSTDIR\${EXE_GUI}" 0

  ; --- file type and URL ProgIDs ---
  WriteRegStr SHCTX "Software\Classes\${PROGID_HTML}" "" "Sweef HTML Document"
  WriteRegStr SHCTX "Software\Classes\${PROGID_HTML}" "FriendlyTypeName" "Sweef HTML Document"
  WriteRegStr SHCTX "Software\Classes\${PROGID_HTML}\DefaultIcon" "" "$INSTDIR\${EXE_GUI},0"
  WriteRegStr SHCTX "Software\Classes\${PROGID_HTML}\shell\open\command" "" '"$INSTDIR\${EXE_GUI}" "%1"'

  WriteRegStr SHCTX "Software\Classes\${PROGID_URL}" "" "Sweef URL"
  WriteRegStr SHCTX "Software\Classes\${PROGID_URL}" "URL Protocol" ""
  WriteRegStr SHCTX "Software\Classes\${PROGID_URL}\DefaultIcon" "" "$INSTDIR\${EXE_GUI},0"
  WriteRegStr SHCTX "Software\Classes\${PROGID_URL}\shell\open\command" "" '"$INSTDIR\${EXE_GUI}" "%1"'

  ; --- "Default apps" capabilities (the user still has to pick Sweef in Windows Settings) ---
  WriteRegStr SHCTX "${CLIENT_KEY}" "" "${APPNAME}"
  WriteRegStr SHCTX "${CLIENT_KEY}\DefaultIcon" "" "$INSTDIR\${EXE_GUI},0"
  WriteRegStr SHCTX "${CLIENT_KEY}\shell\open\command" "" '"$INSTDIR\${EXE_GUI}"'
  WriteRegStr SHCTX "${CLIENT_KEY}\InstallInfo" "ReinstallCommand" '"$INSTDIR\${EXE_GUI}"'
  WriteRegDWORD SHCTX "${CLIENT_KEY}\InstallInfo" "IconsVisible" 1
  WriteRegStr SHCTX "${CAPS_KEY}" "ApplicationName" "${APPNAME}"
  WriteRegStr SHCTX "${CAPS_KEY}" "ApplicationDescription" "Fast independent browser with a built-in agent"
  WriteRegStr SHCTX "${CAPS_KEY}" "ApplicationIcon" "$INSTDIR\${EXE_GUI},0"
  WriteRegStr SHCTX "${CAPS_KEY}\FileAssociations" ".htm" "${PROGID_HTML}"
  WriteRegStr SHCTX "${CAPS_KEY}\FileAssociations" ".html" "${PROGID_HTML}"
  WriteRegStr SHCTX "${CAPS_KEY}\FileAssociations" ".xht" "${PROGID_HTML}"
  WriteRegStr SHCTX "${CAPS_KEY}\FileAssociations" ".xhtml" "${PROGID_HTML}"
  WriteRegStr SHCTX "${CAPS_KEY}\URLAssociations" "http" "${PROGID_URL}"
  WriteRegStr SHCTX "${CAPS_KEY}\URLAssociations" "https" "${PROGID_URL}"
  WriteRegStr SHCTX "${CAPS_KEY}\URLAssociations" "ftp" "${PROGID_URL}"
  WriteRegStr SHCTX "${CAPS_KEY}\StartMenu" "StartMenuInternet" "${APPNAME}"
  WriteRegStr SHCTX "Software\RegisteredApplications" "${APPNAME}" "${CAPS_KEY}"

  ; Run dialog / "sweef" from Win+R
  WriteRegStr SHCTX "Software\Microsoft\Windows\CurrentVersion\App Paths\${EXE_CLI}" "" "$INSTDIR\${EXE_CLI}"
  WriteRegStr SHCTX "Software\Microsoft\Windows\CurrentVersion\App Paths\${EXE_CLI}" "Path" "$INSTDIR"
  ; "Open with" list
  WriteRegStr SHCTX "Software\Classes\Applications\${EXE_GUI}\shell\open\command" "" '"$INSTDIR\${EXE_GUI}" "%1"'
  WriteRegStr SHCTX "Software\Classes\Applications\${EXE_GUI}" "FriendlyAppName" "${APPNAME}"

  ; Apps & features entry
  WriteRegStr SHCTX "${UNINST_KEY}" "DisplayName" "${APPNAME} (alpha)"
  WriteRegStr SHCTX "${UNINST_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr SHCTX "${UNINST_KEY}" "Publisher" "${COMPANY}"
  WriteRegStr SHCTX "${UNINST_KEY}" "DisplayIcon" "$INSTDIR\${EXE_GUI}"
  WriteRegStr SHCTX "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr SHCTX "${UNINST_KEY}" "UninstallString" '"$INSTDIR\uninstall.exe" /$MultiUser.InstallMode'
  WriteRegStr SHCTX "${UNINST_KEY}" "QuietUninstallString" '"$INSTDIR\uninstall.exe" /$MultiUser.InstallMode /S'
  WriteRegStr SHCTX "${UNINST_KEY}" "URLInfoAbout" "https://github.com/sweefbrowser/sweef"
  WriteRegDWORD SHCTX "${UNINST_KEY}" "NoModify" 1
  WriteRegDWORD SHCTX "${UNINST_KEY}" "NoRepair" 1
  WriteRegDWORD SHCTX "${UNINST_KEY}" "EstimatedSize" 40000

  ; tell Explorer file associations / capabilities changed
  System::Call 'shell32::SHChangeNotify(i 0x08000000, i 0, p 0, p 0)'
SectionEnd

Section "Desktop shortcut" SecDesktop
  CreateShortcut "$DESKTOP\${APPNAME}.lnk" "$INSTDIR\${EXE_GUI}" "" "$INSTDIR\${EXE_GUI}" 0
SectionEnd

LangString DESC_SecMain ${LANG_ENGLISH} "The ${APPNAME} browser and its command line tools."
LangString DESC_SecDesktop ${LANG_ENGLISH} "Add a ${APPNAME} shortcut to the desktop."
!insertmacro MUI_FUNCTION_DESCRIPTION_BEGIN
  !insertmacro MUI_DESCRIPTION_TEXT ${SecMain} $(DESC_SecMain)
  !insertmacro MUI_DESCRIPTION_TEXT ${SecDesktop} $(DESC_SecDesktop)
!insertmacro MUI_FUNCTION_DESCRIPTION_END

; ---- uninstall ----------------------------------------------------------------
Section "Uninstall"
  SetRegView 64
  Delete "$SMPROGRAMS\${APPNAME}.lnk"
  Delete "$DESKTOP\${APPNAME}.lnk"
  Delete "$INSTDIR\${EXE_CLI}"
  Delete "$INSTDIR\${EXE_GUI}"
  Delete "$INSTDIR\NOTICE"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"

  DeleteRegKey SHCTX "Software\Classes\${PROGID_HTML}"
  DeleteRegKey SHCTX "Software\Classes\${PROGID_URL}"
  DeleteRegKey SHCTX "Software\Classes\Applications\${EXE_GUI}"
  DeleteRegKey SHCTX "${CLIENT_KEY}"
  DeleteRegValue SHCTX "Software\RegisteredApplications" "${APPNAME}"
  DeleteRegKey SHCTX "Software\Microsoft\Windows\CurrentVersion\App Paths\${EXE_CLI}"
  DeleteRegKey SHCTX "${UNINST_KEY}"
  System::Call 'shell32::SHChangeNotify(i 0x08000000, i 0, p 0, p 0)'
  ; User data (%APPDATA%\Sweef, %LOCALAPPDATA%\Sweef) is intentionally kept.
SectionEnd
