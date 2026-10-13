Unicode true
!define VERSION "1.59.0"
!include "MUI2.nsh"
Name "Loadout ${VERSION}"
OutFile "Loadout-Setup-${VERSION}.exe"
InstallDir "$PROGRAMFILES64\Loadout"
InstallDirRegKey HKLM "Software\Loadout" "InstallDir"
RequestExecutionLevel admin
SetCompressor /SOLID lzma
!define MUI_ICON "app.ico"
!define MUI_UNICON "app.ico"
!define MUI_FINISHPAGE_RUN "$INSTDIR\Loadout.exe"
!define MUI_FINISHPAGE_RUN_TEXT "Launch Loadout"
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"
VIProductVersion "${VERSION}.0"
VIAddVersionKey "ProductName" "Loadout"
VIAddVersionKey "FileDescription" "Loadout ${VERSION} Setup"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "LegalCopyright" "Jason Belec"
Section "Install"
  ; a copy still running (often hidden in the tray) would keep the old program in place
  nsExec::Exec 'taskkill /F /IM Loadout.exe'
  nsExec::Exec 'taskkill /F /IM ps5upload-engine.exe'
  Pop $1
  nsExec::Exec 'taskkill /F /IM TitleTag.exe'
  Pop $1
  Sleep 600
  ; the app used to be called TitleTag: clear the old install (settings are kept and moved over on first start)
  ReadRegStr $0 HKLM "Software\TitleTag" "InstallDir"
  StrCmp $0 "" +5
    Delete "$0\TitleTag.exe"
    Delete "$0\Uninstall.exe"
    RMDir "$0"
    DeleteRegKey HKLM "Software\TitleTag"
  Delete "$SMPROGRAMS\TitleTag.lnk"
  Delete "$DESKTOP\TitleTag.lnk"
  DeleteRegKey HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\TitleTag"
  StrCmp $INSTDIR "$PROGRAMFILES64\TitleTag" 0 +2
    StrCpy $INSTDIR "$PROGRAMFILES64\Loadout"
  SetOutPath "$INSTDIR"
  File "Loadout.exe"
  File "ps5upload-engine.exe"
  File "PS5Upload-LICENSE.txt"
  File "PS5Upload-NOTICE.txt"
  ; the Loadout app for the PS5, which Settings › Phone remote installs on the console
  RMDir /r "$INSTDIR\PS5App"
  SetOutPath "$INSTDIR\PS5App"
  File /r "ps5app\dist\PPSA99880"
  SetOutPath "$INSTDIR"
  WriteUninstaller "$INSTDIR\Uninstall.exe"
  CreateShortcut "$SMPROGRAMS\Loadout.lnk" "$INSTDIR\Loadout.exe"
  CreateShortcut "$DESKTOP\Loadout.lnk" "$INSTDIR\Loadout.exe"
  WriteRegStr HKLM "Software\Loadout" "InstallDir" "$INSTDIR"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\Loadout" "DisplayName" "Loadout"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\Loadout" "DisplayIcon" "$INSTDIR\Loadout.exe"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\Loadout" "Publisher" "Jason Belec"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\Loadout" "DisplayVersion" "${VERSION}"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\Loadout" "UninstallString" '"$INSTDIR\Uninstall.exe"'
SectionEnd
Section "Uninstall"
  Delete "$INSTDIR\Loadout.exe"
  Delete "$INSTDIR\ps5upload-engine.exe"
  Delete "$INSTDIR\PS5Upload-LICENSE.txt"
  Delete "$INSTDIR\PS5Upload-NOTICE.txt"
  Delete "$INSTDIR\Uninstall.exe"
  RMDir /r "$INSTDIR\PS5App"
  RMDir "$INSTDIR"
  Delete "$SMPROGRAMS\Loadout.lnk"
  Delete "$DESKTOP\Loadout.lnk"
  DeleteRegKey HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\Loadout"
  DeleteRegKey HKLM "Software\Loadout"
SectionEnd
