@echo off
set TASKNAME=PixelArcadeProxy
schtasks /End /TN "%TASKNAME%" >nul 2>&1
schtasks /Delete /TN "%TASKNAME%" /F
echo Removed.
pause
