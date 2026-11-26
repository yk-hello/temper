@echo off
rem Keep a strategy running on Windows: restart it 10 seconds after it exits or crashes.
rem Put this file, strategy.env and your strategy (with qsclient.py) in one folder and double-click it.
rem Closing this window stops the strategy. Output goes to strategy.log next to this file.
rem To start it when you sign in: press Win+R, type shell:startup, put a shortcut to this file there.
rem See docs/running.md.

chcp 65001 >nul
cd /d "%~dp0"
set "SCRIPT=template_rule.py"
if not "%~1"=="" set "SCRIPT=%~1"
if not exist strategy.env (
  echo strategy.env not found. Copy strategy.env.example to strategy.env and fill it in.
  pause
  exit /b 1
)
for /f "usebackq eol=# tokens=1,* delims==" %%a in ("strategy.env") do set "%%a=%%b"
set PYTHONUTF8=1
set PYTHONUNBUFFERED=1

:loop
echo [%date% %time%] start %SCRIPT%>> strategy.log
python "%SCRIPT%" >> strategy.log 2>&1
echo [%date% %time%] %SCRIPT% exited with code %errorlevel%, restarting in 10 seconds>> strategy.log
python -c "import time; time.sleep(10)"
goto loop
