// ============================================================================
//  Pixel Arcade - Image Proxy
//
//  Receives the current game's Clear Logo file path from the LaunchBox plugin
//  via POST /set (empty body => show the default image). Reads the image
//  straight off disk, converts it to a 480x320 RGB565 stream, and serves it
//  to the ESP32 on /current.
//
//  Config is read from "config.ini" next to this exe (auto-created on first
//  run). Build (hidden, no console):
//    GOOS=windows GOARCH=amd64 go build -ldflags="-s -w -H windowsgui" \
//      -o arcade-imgproxy.exe main.go
// ============================================================================

package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ── configuration ─────────────────────────────────────────────────────────
type Config struct {
	ProxyPort  string // port this proxy listens on
	DefaultImg string // local path to the image shown when nothing is selected
	OutW       int
	OutH       int
	Rotate     float64
	ImagesRoot string // LaunchBox Images folder, served as plain files for Home Assistant
}

var cfg Config

const configTemplate = `# ============================================================
#  Pixel Arcade - Shared Configuration (proxy + LaunchBox plugin)
#  Edit these values, then restart LaunchBox / the proxy.
# ============================================================

# Port the proxy listens on (the ESP connects here). Usually leave as-is.
proxy_port = 8090

# Address the PLUGIN posts to. If proxy + LaunchBox are on the SAME PC,
# leave 127.0.0.1. Keep the :PORT/set part matching proxy_port above.
proxy_url = http://127.0.0.1:8090/set

# Local image shown when no game/platform is selected.
# Put a default.png next to this exe, or give a full path.
default_image = default.png

# Display resolution. ILI9488 is 480x320.
display_width  = 480
display_height = 320

# Rotate image degrees to compensate a crooked screen. 0 = none. e.g. -1
rotate_degrees = 0

# OPTIONAL - Home Assistant / browser support.
# Point this at your LaunchBox Images folder and the proxy also serves those
# files as ordinary PNG/JPEG on the SAME port, e.g.
#   http://<pc-ip>:8090/Clear%20Logo/Arcade/Pac-Man.png
# That replaces the second image server some MQTT plugins run on :8089 -
# just swap the port in your Home Assistant templates. Leave blank to disable.
images_root =
`

func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

func loadConfig() {
	cfg = Config{ProxyPort: "8090", DefaultImg: "default.png", OutW: 480, OutH: 320, Rotate: 0}

	path := filepath.Join(exeDir(), "config.ini")
	f, err := os.Open(path)
	if err != nil {
		_ = os.WriteFile(path, []byte(configTemplate), 0644)
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		switch key {
		case "proxy_port":
			cfg.ProxyPort = val
		case "default_image":
			cfg.DefaultImg = val
		case "display_width":
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				cfg.OutW = n
			}
		case "display_height":
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				cfg.OutH = n
			}
		case "rotate_degrees":
			if fv, err := strconv.ParseFloat(val, 64); err == nil {
				cfg.Rotate = fv
			}
		case "images_root":
			cfg.ImagesRoot = val
		}
	}
}

// Resolve the default image to an absolute path (relative = next to exe).
func defaultPath() string {
	if filepath.IsAbs(cfg.DefaultImg) {
		return cfg.DefaultImg
	}
	return filepath.Join(exeDir(), cfg.DefaultImg)
}

// ── state + change notification ───────────────────────────────────────────────
var (
	mu          sync.RWMutex
	currentPath string // local file path of the image to show
	curTitle    string // game title, for Home Assistant
	curPlatform string // platform name, for Home Assistant
	curMarquee  string // local file path of the marquee, for Home Assistant
	version     = 0
	changed     = make(chan struct{})
	lastGroup   = ""
)

// What the LaunchBox plugin POSTs to /set. A bare path (no leading "{") is
// still accepted, so an older plugin keeps working against a newer proxy.
type setPayload struct {
	Title    string `json:"title"`
	Platform string `json:"platform"`
	Logo     string `json:"logo"`
	Marquee  string `json:"marquee"`
}

// Metadata only - deliberately does NOT touch "version". The version counter
// is what the ESP polls, so it must move only when the *image* changes;
// bumping it here would make the screen reload for a title-only change.
func setMeta(title, platform, marquee string) {
	mu.Lock()
	defer mu.Unlock()
	curTitle, curPlatform, curMarquee = title, platform, marquee
}

// Matches LaunchBox sequences like "-01.png", "-02.jpg", or just ".png"
var suffixRe = regexp.MustCompile(`(-\d+)?\.[a-zA-Z0-9]+$`)

func setCurrent(path string) {
	mu.Lock()
	defer mu.Unlock()

	if path == "" {
		path = defaultPath()
	}
	if path == currentPath {
		return
	}

	// Ignore cycled variants of the same game's logo (-01, -02, ...)
	group := suffixRe.ReplaceAllString(path, "")
	if group == lastGroup && path != defaultPath() {
		return
	}

	lastGroup = group
	currentPath = path
	version++
	close(changed)
	changed = make(chan struct{})
}

func snapshot() (string, int, chan struct{}) {
	mu.RLock()
	defer mu.RUnlock()
	return currentPath, version, changed
}

func snapshotMeta() (path, title, platform, marquee string, ver int) {
	mu.RLock()
	defer mu.RUnlock()
	return currentPath, curTitle, curPlatform, curMarquee, version
}

// ── image processing ──────────────────────────────────────────────────────────
func fitResize(src image.Image, maxW, maxH int) *image.NRGBA {
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	scale := float64(maxW) / float64(sw)
	if s := float64(maxH) / float64(sh); s < scale {
		scale = s
	}
	dw := int(float64(sw) * scale)
	dh := int(float64(sh) * scale)
	if dw < 1 { dw = 1 }
	if dh < 1 { dh = 1 }

	flat := image.NewNRGBA(src.Bounds())
	draw.Draw(flat, flat.Bounds(), image.NewUniform(color.Black), image.Point{}, draw.Src)
	draw.Draw(flat, flat.Bounds(), src, src.Bounds().Min, draw.Over)

	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		for x := 0; x < dw; x++ {
			dst.SetNRGBA(x, y, flat.NRGBAAt(int(float64(x)/scale), int(float64(y)/scale)))
		}
	}
	return dst
}

func renderCanvas(fitted *image.NRGBA, deg float64) *image.NRGBA {
	canvas := image.NewNRGBA(image.Rect(0, 0, cfg.OutW, cfg.OutH))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.Black), image.Point{}, draw.Src)
	fw, fh := fitted.Bounds().Dx(), fitted.Bounds().Dy()
	cx, cy := float64(cfg.OutW)/2, float64(cfg.OutH)/2
	rad := deg * math.Pi / 180
	cosA, sinA := math.Cos(rad), math.Sin(rad)
	for y := 0; y < cfg.OutH; y++ {
		for x := 0; x < cfg.OutW; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			fx := int(cosA*dx + sinA*dy + float64(fw)/2)
			fy := int(-sinA*dx + cosA*dy + float64(fh)/2)
			if fx >= 0 && fx < fw && fy >= 0 && fy < fh {
				canvas.SetNRGBA(x, y, fitted.NRGBAAt(fx, fy))
			}
		}
	}
	return canvas
}

func encodeRGB565(canvas *image.NRGBA) []byte {
	// RLE format (big-endian):
	//   [W u16][H u16]
	//   then runs of: [count u16][pixel u16]
	// "count" pixels (row-major, left-to-right, top-to-bottom) share "pixel".
	// Long black runs (margins / gaps) compress massively -> far less WiFi.
	out := make([]byte, 0, 1<<16)
	var hdr [4]byte
	binary.BigEndian.PutUint16(hdr[0:], uint16(cfg.OutW))
	binary.BigEndian.PutUint16(hdr[2:], uint16(cfg.OutH))
	out = append(out, hdr[:]...)

	total := cfg.OutW * cfg.OutH
	var run [4]byte

	getPx := func(i int) uint16 {
		x := i % cfg.OutW
		y := i / cfg.OutW
		c := canvas.NRGBAAt(x, y)
		r5, g6, b5 := uint16(c.R)>>3, uint16(c.G)>>2, uint16(c.B)>>3
		return (r5 << 11) | (g6 << 5) | b5
	}

	i := 0
	for i < total {
		px := getPx(i)
		count := 1
		for i+count < total && count < 65535 && getPx(i+count) == px {
			count++
		}
		binary.BigEndian.PutUint16(run[0:], uint16(count))
		binary.BigEndian.PutUint16(run[2:], px)
		out = append(out, run[:]...)
		i += count
	}
	return out
}

func serveImage(path string, w http.ResponseWriter) {
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "open error", 404)
		return
	}
	defer f.Close()

	src, _, err := image.Decode(f)
	if err != nil {
		http.Error(w, "decode error", 500)
		return
	}

	fitW, fitH := cfg.OutW, cfg.OutH
	if cfg.Rotate != 0 {
		fitW, fitH = cfg.OutW-24, cfg.OutH-24
	}
	fitted := fitResize(src, fitW, fitH)
	canvas := renderCanvas(fitted, cfg.Rotate)
	out := encodeRGB565(canvas)

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(out)))
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(out)
}

// ── HTTP handlers ──────────────────────────────────────────────────────────

// POST /set — either a JSON setPayload, or (legacy) a bare clear-logo path.
// Empty => show the default image.
func handleSet(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 8192))
	s := strings.TrimSpace(string(body))

	if strings.HasPrefix(s, "{") {
		var p setPayload
		if err := json.Unmarshal([]byte(s), &p); err == nil {
			setMeta(strings.TrimSpace(p.Title), strings.TrimSpace(p.Platform), strings.TrimSpace(p.Marquee))
			setCurrent(strings.TrimSpace(p.Logo))
			w.WriteHeader(200)
			w.Write([]byte("ok"))
			return
		}
	}

	setMeta("", "", "") // legacy bare path carries no metadata
	setCurrent(s)
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

// Turn a local file path into a URL served by this proxy, using the Host the
// caller reached us on so it works from any IP without configuration.
// Returns "" if the file is not inside images_root.
func imageURL(host, path string) string {
	if cfg.ImagesRoot == "" || path == "" {
		return ""
	}
	rel, err := filepath.Rel(cfg.ImagesRoot, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i, seg := range parts {
		parts[i] = url.PathEscape(seg)
	}
	return "http://" + host + "/" + strings.Join(parts, "/")
}

// GET /status — everything Home Assistant needs, as ready-made URLs so no
// path rewriting is required on the HA side.
func handleStatus(w http.ResponseWriter, r *http.Request) {
	path, title, platform, marquee, ver := snapshotMeta()

	logoURL := imageURL(r.Host, path)
	if logoURL == "" {
		// Not under images_root (e.g. the default image) - fall back to the
		// always-valid live endpoint.
		logoURL = "http://" + r.Host + "/current.png"
	}
	marqueeURL := imageURL(r.Host, marquee)
	if marqueeURL == "" {
		marqueeURL = logoURL
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store, must-revalidate")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"version":      ver,
		"title":        title,
		"platform":     platform,
		"logo_url":     logoURL,
		"marquee_url":  marqueeURL,
		"logo_path":    path,
		"marquee_path": marquee,
	})
}

// GET /current — serve the current image as RGB565
func handleCurrent(w http.ResponseWriter, r *http.Request) {
	path, _, _ := snapshot()
	serveImage(path, w)
}

// GET /wait?v=N — long-poll for instant updates (optional; firmware uses /version)
func handleWait(w http.ResponseWriter, r *http.Request) {
	clientVer := r.URL.Query().Get("v")
	_, ver, ch := snapshot()
	if clientVer != fmt.Sprintf("%d", ver) {
		fmt.Fprintf(w, "%d", ver)
		return
	}
	select {
	case <-ch:
		_, nv, _ := snapshot()
		fmt.Fprintf(w, "%d", nv)
	case <-time.After(25 * time.Second):
		fmt.Fprintf(w, "%d", ver)
	case <-r.Context().Done():
		return
	}
}

// GET /current.png — the SAME logo the ESP is showing, but as the original
// PNG/JPEG so a browser (i.e. a Home Assistant entity_picture) can render it.
// Deliberately uncached: the URL never changes, so a cached copy would show
// the previous game forever.
func handleCurrentPNG(w http.ResponseWriter, r *http.Request) {
	path, _, _ := snapshot()
	data, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, "open error", 404)
		return
	}
	ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if ct == "" {
		ct = "image/png"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.Header().Set("Cache-Control", "no-store, must-revalidate")
	w.Write(data)
}

// GET /version — current version number
func handleVersion(w http.ResponseWriter, r *http.Request) {
	_, ver, _ := snapshot()
	fmt.Fprintf(w, "%d", ver)
}

func main() {
	loadConfig()
	currentPath = defaultPath()

	http.HandleFunc("/set", handleSet)
	http.HandleFunc("/current", handleCurrent)
	http.HandleFunc("/current.png", handleCurrentPNG)
	http.HandleFunc("/status", handleStatus)
	http.HandleFunc("/wait", handleWait)
	http.HandleFunc("/version", handleVersion)

	// Optional static file server for the LaunchBox Images folder, so Home
	// Assistant can pull logos/marquees straight off this port instead of a
	// second server. The explicit routes above are exact matches and always
	// win over this catch-all. http.Dir refuses to escape the root.
	if cfg.ImagesRoot != "" {
		if st, err := os.Stat(cfg.ImagesRoot); err == nil && st.IsDir() {
			http.Handle("/", http.FileServer(http.Dir(cfg.ImagesRoot)))
		}
	}

	http.ListenAndServe(":"+cfg.ProxyPort, nil)
}
