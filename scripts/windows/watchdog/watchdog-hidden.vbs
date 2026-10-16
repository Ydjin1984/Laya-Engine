' Laya watchdog -- hidden start, no console window.
'
' Launched from Task Scheduler (setup-watchdog.ps1) as:
'     wscript.exe watchdog-hidden.vbs
' A plain "powershell -WindowStyle Hidden" still flashes a console window,
' because Windows Terminal is the default console host; window style 0 in
' WScript.Shell.Run creates it hidden from the start.
'
' The path to watchdog-laya.ps1 is resolved relative to this file, so the
' repository can live anywhere.

Dim fso, here, script
Set fso = CreateObject("Scripting.FileSystemObject")
here = fso.GetParentFolderName(WScript.ScriptFullName)
script = fso.BuildPath(here, "watchdog-laya.ps1")

CreateObject("WScript.Shell").Run _
  "powershell.exe -NoProfile -ExecutionPolicy Bypass -File """ & script & """", 0, False
