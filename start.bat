@echo off
rem Mediasorter for Windows: double-click to set up and start (again: to update / restart).
cd /d "%~dp0"

where docker >nul 2>nul
if errorlevel 1 (
  echo Docker is not installed. Install Docker Desktop: https://www.docker.com/products/docker-desktop/
  echo Then run start.bat again.
  start "" https://www.docker.com/products/docker-desktop/
  pause
  exit /b 1
)
docker info >nul 2>nul
if errorlevel 1 (
  echo Docker Desktop is not running. Start it, wait until it says "Engine running", then run start.bat again.
  pause
  exit /b 1
)

rem first run (or the folder is gone): a folder dialog asks where the photos are
powershell -NoProfile -STA -ExecutionPolicy Bypass -File tools\choose-folder.ps1
if errorlevel 1 (
  pause
  exit /b 1
)

set FILES=-f docker-compose.yml
where nvidia-smi >nul 2>nul && set FILES=%FILES% -f docker-compose.gpu.yml
findstr /r /c:"^NAS_SHARE=..*" .env >nul && set FILES=%FILES% -f docker-compose.nas.yml

echo Building and starting. The first time takes 10-30 minutes (it downloads ~5 GB).
docker compose %FILES% up -d --build
if errorlevel 1 (
  echo Something went wrong, see the messages above.
  pause
  exit /b 1
)
for /f "tokens=2 delims==" %%p in ('findstr /r /c:"^PORT=" .env') do set PORT=%%p
if "%PORT%"=="" set PORT=8501
echo.
echo Ready: http://localhost:%PORT%
echo Photos are being scanned in the background (about 1 hour per 20 000 files on a GPU).
echo Progress: docker compose logs -f     Stop: docker compose down
start "" http://localhost:%PORT%
pause
