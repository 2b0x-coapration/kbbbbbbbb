package main

import (
	"archive/zip"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
)

//go:embed ui.html
var ui []byte

//go:embed app.zip
var appZip []byte

const ev = "22.3.27"
const xv = "v1.8.4"

var (
	mu   sync.Mutex
	subs []chan string
	last string
	dir  string
	un   bool
	clean bool
)

const regKey = `HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall\SUPERGO`

func hidden(name string, a ...string) {
	c := exec.Command(name, a...)
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	c.Run()
}

func register() {
	self, _ := os.Executable()
	b, _ := os.ReadFile(self)
	u := filepath.Join(dir, "Uninstall SUPERGO.exe")
	os.WriteFile(u, b, 0o755)
	for _, kv := range [][2]string{{"DisplayName", "SUPERGO"}, {"DisplayVersion", "2.0.0"}, {"Publisher", "SUPERGO"}, {"InstallLocation", dir},
		{"DisplayIcon", filepath.Join(dir, "resources", "app", "icon.ico")}, {"UninstallString", `"` + u + `" --uninstall`}} {
		hidden("reg", "add", regKey, "/v", kv[0], "/t", "REG_SZ", "/d", kv[1], "/f")
	}
}

func remove(data bool) {
	emit("stop", 5, "Closing SUPERGO")
	hidden("taskkill", "/f", "/im", "SUPERGO.exe")
	emit("shortcuts", 30, "Removing shortcuts")
	hidden("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command", `Remove-Item (Join-Path ([Environment]::GetFolderPath('Programs')) 'SUPERGO.lnk') -Force -ErrorAction SilentlyContinue; Remove-Item (Join-Path ([Environment]::GetFolderPath('Desktop')) 'SUPERGO.lnk') -Force -ErrorAction SilentlyContinue`)
	emit("registry", 55, "Removing from Apps & features")
	hidden("reg", "delete", regKey, "/f")
	if data {
		emit("data", 70, "Deleting your browser data")
		os.RemoveAll(filepath.Join(os.Getenv("APPDATA"), "SUPERGO"))
	}
	emit("files", 85, "Removing program files")
	clean = true
	emit("done", 100, "SUPERGO was removed")
}

func emit(step string, pct float64, msg string) {
	b, _ := json.Marshal(map[string]any{"step": step, "pct": pct, "msg": msg})
	mu.Lock()
	last = string(b)
	for _, c := range subs {
		select {
		case c <- last:
		default:
		}
	}
	mu.Unlock()
}

func fail(err error) { emit("error", 0, err.Error()) }

func get(url string, step string, lo, hi float64, label string) ([]byte, error) {
	r, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return nil, fmt.Errorf("%s: HTTP %d", label, r.StatusCode)
	}
	var out []byte
	buf := make([]byte, 64<<10)
	for {
		n, e := r.Body.Read(buf)
		out = append(out, buf[:n]...)
		if r.ContentLength > 0 {
			f := float64(len(out)) / float64(r.ContentLength)
			emit(step, lo+(hi-lo)*f, fmt.Sprintf("%s  %.1f / %.1f MB", label, float64(len(out))/1e6, float64(r.ContentLength)/1e6))
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
	}
	return out, nil
}

func unzipTo(b []byte, dst string, strip bool, pick func(string) bool) error {
	z, err := zip.NewReader(strings.NewReader(string(b)), int64(len(b)))
	if err != nil {
		return err
	}
	for _, f := range z.File {
		if f.FileInfo().IsDir() || (pick != nil && !pick(f.Name)) {
			continue
		}
		p := filepath.Join(dst, filepath.FromSlash(f.Name))
		if !strings.HasPrefix(p, filepath.Clean(dst)+string(os.PathSeparator)) {
			return fmt.Errorf("unsafe path in archive")
		}
		os.MkdirAll(filepath.Dir(p), 0o755)
		rc, _ := f.Open()
		w, err := os.Create(p)
		if err != nil {
			return err
		}
		io.Copy(w, rc)
		w.Close()
		rc.Close()
	}
	return nil
}

func install(vpn bool) {
	arch := "x64"
	xa := "64"
	if runtime.GOARCH == "386" {
		arch, xa = "ia32", "32"
	}
	emit("prepare", 2, "Preparing "+dir)
	os.MkdirAll(dir, 0o755)
	base := "https://github.com/electron/electron/releases/download/v" + ev + "/"
	name := "electron-v" + ev + "-win32-" + arch + ".zip"
	sums, err := get(base+"SHASUMS256.txt", "engine", 3, 5, "Checking release")
	if err != nil {
		fail(err)
		return
	}
	want := ""
	for _, l := range strings.Split(string(sums), "\n") {
		if f := strings.Fields(l); len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			want = f[0]
		}
	}
	zb, err := get(base+name, "engine", 5, 70, "Downloading browser engine")
	if err != nil {
		fail(err)
		return
	}
	emit("verify", 71, "Verifying download")
	h := sha256.Sum256(zb)
	if want == "" || hex.EncodeToString(h[:]) != want {
		fail(fmt.Errorf("Checksum mismatch - the engine file is corrupted. Nothing was installed."))
		return
	}
	emit("unpack", 75, "Unpacking engine")
	if err := unzipTo(zb, dir, false, nil); err != nil {
		fail(err)
		return
	}
	os.Rename(filepath.Join(dir, "electron.exe"), filepath.Join(dir, "SUPERGO.exe"))
	os.Remove(filepath.Join(dir, "resources", "default_app.asar"))
	emit("app", 85, "Installing SUPERGO")
	if err := unzipTo(appZip, filepath.Join(dir, "resources", "app"), false, nil); err != nil {
		fail(err)
		return
	}
	if vpn {
		xb, err := get("https://github.com/XTLS/Xray-core/releases/download/"+xv+"/Xray-windows-"+xa+".zip", "vpn", 88, 94, "Downloading VPN core")
		if err == nil {
			unzipTo(xb, filepath.Join(dir, "vpn"), false, func(n string) bool { return n == "xray.exe" || strings.HasSuffix(n, ".dat") })
		}
	}
	emit("shortcuts", 96, "Creating shortcuts")
	exe := filepath.Join(dir, "SUPERGO.exe")
	ps := fmt.Sprintf(`$w=New-Object -ComObject WScript.Shell; foreach($l in @((Join-Path ([Environment]::GetFolderPath('Programs')) 'SUPERGO.lnk'),(Join-Path ([Environment]::GetFolderPath('Desktop')) 'SUPERGO.lnk'))){$s=$w.CreateShortcut($l);$s.TargetPath='%s';$s.WorkingDirectory='%s';$s.IconLocation='%s,0';$s.Save()}`, exe, dir, filepath.Join(dir, "resources", "app", "icon.ico"))
	c := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command", ps)
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	c.Run()
	emit("registry", 98, "Registering uninstaller")
	register()
	emit("done", 100, "SUPERGO is ready")
}

func main() {
	dir = filepath.Join(os.Getenv("LOCALAPPDATA"), "SUPERGO")
	if len(os.Args) > 1 && os.Args[1] == "--uninstall" {
		un = true
		self, _ := os.Executable()
		dir = filepath.Dir(self)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Content-Type", "text/html"); w.Write(ui) })
	http.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		c := make(chan string, 64)
		mu.Lock()
		subs = append(subs, c)
		if last != "" {
			c <- last
		}
		mu.Unlock()
		for {
			select {
			case m := <-c:
				fmt.Fprintf(w, "data: %s\n\n", m)
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
				return
			}
		}
	})
	http.HandleFunc("/mode", func(w http.ResponseWriter, r *http.Request) {
		if un {
			w.Write([]byte("uninstall"))
		} else {
			w.Write([]byte("install"))
		}
	})
	http.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		if un {
			go remove(r.URL.Query().Get("data") == "1")
		} else {
			go install(r.URL.Query().Get("vpn") == "1")
		}
	})
	http.HandleFunc("/launch", func(w http.ResponseWriter, r *http.Request) {
		exec.Command(filepath.Join(dir, "SUPERGO.exe")).Start()
	})
	http.HandleFunc("/quit", func(w http.ResponseWriter, r *http.Request) {
		if clean {
			c := exec.Command("cmd", "/c", `ping -n 3 127.0.0.1 >nul & rmdir /s /q "`+dir+`"`)
			c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
			c.Start()
		}
		os.Exit(0)
	})
	url := "http://" + ln.Addr().String()
	go func() {
		for _, b := range []string{`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`, `C:\Program Files\Microsoft\Edge\Application\msedge.exe`} {
			if _, e := os.Stat(b); e == nil {
				exec.Command(b, "--app="+url, "--window-size=760,520").Start()
				return
			}
		}
		exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	}()
	http.Serve(ln, nil)
}
