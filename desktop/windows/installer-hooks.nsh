; Hooks for the Tauri NSIS installer (bundle.windows.nsis.installerHooks).
;
; The baum daemon outlives the app and runs from $INSTDIR\baum.exe, so the
; installer could not replace or remove that file while it runs. Stop it
; first (hidden, through nsExec); the app's next `baum daemon ensure` starts
; it again. Generation jobs still running are recorded as interrupted; an
; in-app update waits for them before it gets here. Labels are local to the
; install and uninstall sections each hook expands into.

!macro NSIS_HOOK_PREINSTALL
  IfFileExists "$INSTDIR\baum.exe" 0 baum_preinstall_done
    nsExec::ExecToLog '"$INSTDIR\baum.exe" daemon stop'
    Pop $R9 ; the exit code; a daemon that is not running is fine
  baum_preinstall_done:
!macroend

!macro NSIS_HOOK_PREUNINSTALL
  IfFileExists "$INSTDIR\baum.exe" 0 baum_preuninstall_done
    nsExec::ExecToLog '"$INSTDIR\baum.exe" daemon stop'
    Pop $R9
  baum_preuninstall_done:
!macroend
