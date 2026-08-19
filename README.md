# Pixel Arcade Display

Who said it was impossible?!

Shows the Clear Logo of whatever you select in LaunchBox — games *and*
platforms — on a cheap ILI9488 screen driven by an ESP32. Updates instantly.

It can also feed the same images to Home Assistant, with no extra plugins
and no second image server.

```
LaunchBox plugin  ──>  arcade-imgproxy.exe  ──>  ESP32 + ILI9488 screen
 (sends logo path)     (reads file, RGB565)        (displays it)
                              │
                              └──────────────────>  Home Assistant
                                  (/status + images, optional)
```

## 👉 Setup

**Open `INSTALL.txt`.** It walks you through it step by step with a check
after each stage. Roughly 15 minutes.

The short version:

| Part | What | Where the files go |
|------|------|--------------------|
| 1 | PC / LaunchBox | copy `LaunchBox Plugin\PixelArcade` → `D:\LaunchBox\Plugins\` |
| 2 | ESP32 screen | copy both files in `ESPHome\` → your ESPHome folder |
| 3 | Home Assistant *(optional)* | paste `Home Assistant\homeassistant.yaml` into `configuration.yaml` |

Nothing needs compiling — the `.exe` and `.dll` are already built.
`Source\` is only there if you want to modify it.

## Wiring

| Display | ESP32   |
|---------|---------|
| VCC     | 3.3V    |
| GND     | GND     |
| CS      | GPIO21  |
| RESET   | GPIO4   |
| DC      | GPIO27  |
| MOSI    | GPIO23  |
| CLK     | GPIO18  |
| LED/BL  | GPIO32  |

## Home Assistant

You do **not** need the LaunchBox MQTT plugin or a separate image server
(lbImageServer) alongside this. Point `images_root` in `config.ini` at your
LaunchBox Images folder and the proxy serves the images itself, plus a
`/status` endpoint with everything Home Assistant needs:

```json
{
  "version": 12,
  "title": "Pac-Man",
  "platform": "Arcade",
  "logo_url": "http://192.168.1.60:8090/Clear%20Logo/Arcade/Pac-Man.png",
  "marquee_url": "http://192.168.1.60:8090/Arcade%20-%20Marquee/Arcade/Pac-Man.png"
}
```

The URLs are built from whatever address you called it on, so they just work
from any device — no path rewriting in your templates.

*Already using lbImageServer and want to keep it?* You don't have to change
your existing templates at all — the proxy serves the Images folder in the
same layout, so swapping `8089` to `8090` is enough.

## Endpoints

| | |
|---|---|
| `/status` | what's playing, as JSON |
| `/current.png` | the logo on the screen right now, as a real image |
| `/version` | a number that changes on every switch |
| `/current` | the RGB565 stream for the ESP32 |

## Done

Select a game in LaunchBox → its logo appears. Deselect or exit → the
default image.

Sources included. Do whatever you want with it.
