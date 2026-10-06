@echo off
rem Needs Go 1.22+ (https://go.dev/dl)
go install github.com/tc-hib/go-winres@latest || exit /b 1
"%USERPROFILE%\go\bin\go-winres" make --in winres.json --arch amd64,386 || exit /b 1
set GOOS=windows
set GOARCH=amd64
go build -ldflags "-H windowsgui -s -w" -o SUPERGO-Setup-2_0_0.exe . || exit /b 1
set GOARCH=386
go build -ldflags "-H windowsgui -s -w" -o SUPERGO-Setup-2_0_0-x86.exe .
echo Done.
