Option Explicit
Dim shell, fso, repo
Set shell = CreateObject("WScript.Shell")
Set fso = CreateObject("Scripting.FileSystemObject")
repo = fso.GetParentFolderName(WScript.ScriptFullName)
If Not fso.FileExists(repo & "\mirasim-workbench.exe") Then
  MsgBox "mirasim-workbench.exe not found. Run tools\build.ps1 first.", 16, "Mirasim"
  WScript.Quit 1
End If
shell.CurrentDirectory = repo
shell.Run """" & repo & "\mirasim-workbench.exe""", 1, False
