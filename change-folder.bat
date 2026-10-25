@echo off
rem Mediasorter for Windows: choose another photo folder and restart.
cd /d "%~dp0"
powershell -NoProfile -STA -ExecutionPolicy Bypass -File tools\choose-folder.ps1 -Force
if errorlevel 1 (
  pause
  exit /b 1
)
call start.bat
