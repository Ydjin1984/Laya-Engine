@echo off
REM ===========================================================================
REM Laya instance on port 8095 (RTX 3060, GPU #1) for the arcade.
REM Thin wrapper: checks, logging and waiting for /ready live in start-laya.cmd.
REM ASCII-only and CRLF line endings are required.
REM ===========================================================================
call "%~dp0..\..\..\start-laya.cmd" 8095
exit /b %errorlevel%
