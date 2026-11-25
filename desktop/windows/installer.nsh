; Custom macros for electron-builder's NSIS installer (nsis.include in
; electron-builder.mjs).
;
; Stop the user's baum daemon before installing or uninstalling. It outlives
; the app; the app runs it from its own copy of baum.exe in the local data
; directory (%LOCALAPPDATA%\com.usebaum.desktop\bin), or from the installed
; app's resources\bin\baum.exe. `baum daemon stop` reaches the running daemon
; through its control socket whichever binary it runs from, so either copy
; stops it; both are tried, and stopping a daemon that is not running is fine.
; Runs hidden, through nsExec. The app's next `baum daemon ensure` starts the
; daemon again. Generation jobs still running are recorded as interrupted; an
; in-app update waits for them before it gets here.
;
; customInit runs in .onInit and customUnInit in un.onInit, both after
; $INSTDIR is set for the current user. Labels are local to the function each
; macro expands into, and each function inserts baumDaemonStop once.

!macro baumDaemonStop
  IfFileExists "$INSTDIR\resources\bin\baum.exe" 0 baum_stop_data
    nsExec::ExecToLog '"$INSTDIR\resources\bin\baum.exe" daemon stop'
    Pop $R9 ; the exit code
  baum_stop_data:
  IfFileExists "$LOCALAPPDATA\com.usebaum.desktop\bin\baum.exe" 0 baum_stop_done
    nsExec::ExecToLog '"$LOCALAPPDATA\com.usebaum.desktop\bin\baum.exe" daemon stop'
    Pop $R9
  baum_stop_done:
!macroend

!macro customInit
  !insertmacro baumDaemonStop
!macroend

!macro customUnInit
  !insertmacro baumDaemonStop
!macroend
