@echo off
REM ============================================================
REM  Pixel Arcade - Auto-start the proxy at login (hidden)
REM  Right-click this file -> "Run as administrator"
REM ============================================================
set TASKNAME=PixelArcadeProxy
set EXEPATH=%~dp0arcade-imgproxy.exe

echo Installing auto-start for: %EXEPATH%
schtasks /Delete /TN "%TASKNAME%" /F >nul 2>&1

set XML=%TEMP%\pixelarcade_task.xml
(
echo ^<?xml version="1.0" encoding="UTF-16"?^>
echo ^<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task"^>
echo   ^<Triggers^>
echo     ^<LogonTrigger^>^<Enabled^>true^</Enabled^>^</LogonTrigger^>
echo     ^<BootTrigger^>^<Enabled^>true^</Enabled^>^</BootTrigger^>
echo   ^</Triggers^>
echo   ^<Principals^>^<Principal id="Author"^>^<LogonType^>InteractiveToken^</LogonType^>^<RunLevel^>HighestAvailable^</RunLevel^>^</Principal^>^</Principals^>
echo   ^<Settings^>
echo     ^<MultipleInstancesPolicy^>IgnoreNew^</MultipleInstancesPolicy^>
echo     ^<StartWhenAvailable^>true^</StartWhenAvailable^>
echo     ^<RestartOnFailure^>^<Interval^>PT1M^</Interval^>^<Count^>999^</Count^>^</RestartOnFailure^>
echo     ^<ExecutionTimeLimit^>PT0S^</ExecutionTimeLimit^>
echo     ^<Hidden^>true^</Hidden^>
echo   ^</Settings^>
echo   ^<Actions Context="Author"^>^<Exec^>^<Command^>%EXEPATH%^</Command^>^</Exec^>^</Actions^>
echo ^</Task^>
) > "%XML%"

schtasks /Create /TN "%TASKNAME%" /XML "%XML%" /F
del "%XML%" >nul 2>&1

if %ERRORLEVEL% EQU 0 (
  echo [OK] Installed. Starting now...
  schtasks /Run /TN "%TASKNAME%"
  echo Done - proxy runs hidden at every boot, and restarts if it crashes.
) else (
  echo [ERROR] Failed. Did you run as Administrator?
)
pause
