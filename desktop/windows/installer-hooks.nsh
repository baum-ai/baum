; Hooks for the Tauri NSIS installer (bundle.windows.nsis.installerHooks).
;
; Stop the user's baum daemon before installing or uninstalling. It outlives
; the app; the app runs it from its own copy of baum.exe in the local data
; directory (%LOCALAPPDATA%\com.usebaum.desktop\bin), and installs from
; before that copy existed ran it from $INSTDIR\baum.exe. `baum daemon stop`
; reaches the running daemon through its control socket whichever binary it
; runs from, so either copy stops it; both are tried, and stopping a daemon
; that is not running is fine. Runs hidden, through nsExec. The app's next
; `baum daemon ensure` starts the daemon again. Generation jobs still running
; are recorded as interrupted; an in-app update waits for them before it gets
; here. Labels are local to the install and uninstall sections each hook
; expands into.

!macro NSIS_HOOK_PREINSTALL
  IfFileExists "$INSTDIR\baum.exe" 0 baum_preinstall_data
    nsExec::ExecToLog '"$INSTDIR\baum.exe" daemon stop'
    Pop $R9 ; the exit code
  baum_preinstall_data:
  IfFileExists "$LOCALAPPDATA\com.usebaum.desktop\bin\baum.exe" 0 baum_preinstall_done
    nsExec::ExecToLog '"$LOCALAPPDATA\com.usebaum.desktop\bin\baum.exe" daemon stop'
    Pop $R9
  baum_preinstall_done:
!macroend

!macro NSIS_HOOK_PREUNINSTALL
  IfFileExists "$INSTDIR\baum.exe" 0 baum_preuninstall_data
    nsExec::ExecToLog '"$INSTDIR\baum.exe" daemon stop'
    Pop $R9
  baum_preuninstall_data:
  IfFileExists "$LOCALAPPDATA\com.usebaum.desktop\bin\baum.exe" 0 baum_preuninstall_done
    nsExec::ExecToLog '"$LOCALAPPDATA\com.usebaum.desktop\bin\baum.exe" daemon stop'
    Pop $R9
  baum_preuninstall_done:
!macroend
