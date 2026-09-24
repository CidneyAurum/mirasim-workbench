Option Explicit
Dim http
On Error Resume Next
Set http = CreateObject("MSXML2.ServerXMLHTTP.6.0")
http.setTimeouts 1000, 1000, 1000, 20000
http.open "POST", "http://127.0.0.1:7901/api/service/quit", False
http.setRequestHeader "X-Mir-Workbench", "1"
http.setRequestHeader "Content-Type", "application/json"
http.send "{}"
If Err.Number <> 0 Then
  MsgBox "Open the workbench first, then use Settings > Stop gateway.", 48, "Mirasim"
ElseIf http.status <> 200 Then
  MsgBox "Unable to stop. Check workbench logs.", 48, "Mirasim"
End If
