WS to UDP - For interfacing AWS MicroVm Lambdas to WireGuard

You only need the Go installer. Walk calls Win32 directly, so there is no Visual Studio, no C compiler, and no MinGW.

1. Download the Windows 64-bit MSI from https://go.dev/dl/ and run it. Leave the defaults, including adding Go to PATH.
2. Close any open terminal and open a new PowerShell so it sees go.
3. Check it with go version.


go build -o wstoudp.exe -ldflags="-H windowsgui"
.\wstoudp.exe


Linux and macOS use the same bridge.go. The command-line build is selected automatically.

go build -o wstoudp
./wstoudp -url wss://example.com/wireguard -token TOKEN

Ctrl-C stops it. WireGuard still uses 127.0.0.1:51820.


