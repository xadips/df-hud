#Requires AutoHotkey v2.0
#SingleInstance Force

DFHud(path) {
    try {
        request := ComObject("WinHttp.WinHttpRequest.5.1")
        request.SetTimeouts(500, 500, 500, 2000)
        request.Open("POST", "http://127.0.0.1:9275" path, false)
        request.Send()
    } catch {
    }
}

PanicKill() {
    pid := WinGetPID("A")
    ProcessClose(pid)
}

; These keys are consumed only while Dead Frontier is focused.
#HotIf WinActive("ahk_exe DeadFrontier.exe")
v::DFHud("/api/widget/map/toggle")
t::DFHud("/api/widget/challenges/toggle")
SC029::DFHud("/api/run/start") ; Physical grave/backtick key.
x::DFHud("/api/xp/reset")
k::DFHud("/api/overlay/toggle")
XButton1::PanicKill() ; Mouse back button: immediate forced game exit.
#HotIf
