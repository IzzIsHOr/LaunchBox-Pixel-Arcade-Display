// ============================================================================
//  Pixel Arcade - LaunchBox Plugin
//
//  Pushes the selected/launched game's Clear Logo + Marquee paths, title and
//  platform to the Pixel Arcade proxy over HTTP. The proxy drives the ESP32
//  screen and exposes the rest on /status for Home Assistant.
//  Updates on selection change, game launch and exit.
//
//  The proxy URL is read from "PixelArcade.ini" placed next to this DLL.
//  If the file is missing, it is created with a default on first load.
//
//  Drop the built DLL into:  <LaunchBox>\Plugins\PixelArcade\PixelArcade.dll
// ============================================================================

using System;
using System.IO;
using System.Linq;
using System.Net.Http;
using System.Reflection;
using System.Text;
using System.Threading.Tasks;
using Unbroken.LaunchBox.Plugins;
using Unbroken.LaunchBox.Plugins.Data;

namespace PixelArcade
{
    public class PixelArcadePlugin : ISystemEventsPlugin, IGameLaunchingPlugin
    {
        private static readonly string proxyUrl = LoadProxyUrl();
        private static readonly HttpClient http = new HttpClient { Timeout = TimeSpan.FromSeconds(3) };

        // ── config ────────────────────────────────────────────────────────
        private static string LoadProxyUrl()
        {
            // Reads the SHARED config.ini that sits next to this DLL
            // (same folder as arcade-imgproxy.exe). Key: proxy_url.
            string fallback = "http://127.0.0.1:8090/set";
            try
            {
                string dir = Path.GetDirectoryName(Assembly.GetExecutingAssembly().Location);
                string iniPath = Path.Combine(dir, "config.ini");
                if (!File.Exists(iniPath)) return fallback;

                foreach (var raw in File.ReadAllLines(iniPath))
                {
                    string line = raw.Trim();
                    if (line.Length == 0 || line.StartsWith("#")) continue;
                    int eq = line.IndexOf('=');
                    if (eq < 0) continue;
                    string key = line.Substring(0, eq).Trim();
                    string val = line.Substring(eq + 1).Trim();
                    if (key == "proxy_url" && val.Length > 0)
                        return val;
                }
            }
            catch { /* ignore, use fallback */ }
            return fallback;
        }

        // ── json ──────────────────────────────────────────────────────────
        // Hand-rolled so the plugin stays a single dependency-free DLL.
        // Escaping backslashes matters here - every path is a Windows path.
        private static string JsonEscape(string s)
        {
            if (string.IsNullOrEmpty(s)) return "";

            const char BS = (char)92; // backslash
            const char QT = (char)34; // double quote

            var sb = new StringBuilder(s.Length + 16);
            foreach (char c in s)
            {
                if      (c == QT)   sb.Append(BS).Append(QT);
                else if (c == BS)   sb.Append(BS).Append(BS);
                else if (c == '\b') sb.Append(BS).Append('b');
                else if (c == '\f') sb.Append(BS).Append('f');
                else if (c == '\n') sb.Append(BS).Append('n');
                else if (c == '\r') sb.Append(BS).Append('r');
                else if (c == '\t') sb.Append(BS).Append('t');
                else if (c < 0x20)  sb.Append(BS).Append('u').Append(((int)c).ToString("x4"));
                else                sb.Append(c);
            }
            return sb.ToString();
        }

        // ── networking ────────────────────────────────────────────────────
        private void Send(string title, string platform, string logo, string marquee)
        {
            string json = "{\"title\":\"" + JsonEscape(title)
                        + "\",\"platform\":\"" + JsonEscape(platform)
                        + "\",\"logo\":\"" + JsonEscape(logo)
                        + "\",\"marquee\":\"" + JsonEscape(marquee) + "\"}";

            Task.Run(async () =>
            {
                try { await http.PostAsync(proxyUrl, new StringContent(json, Encoding.UTF8, "application/json")); }
                catch { /* proxy offline - never disturb LaunchBox */ }
            });
        }

        private void SendGame(IGame game)
        {
            if (game == null) { Send("", "", "", ""); return; }
            Send(game.Title, game.Platform, game.ClearLogoImagePath, game.MarqueeImagePath);
        }

        // Decide what to show based on current selection.
        private void UpdateDisplay()
        {
            // 1. Selected game's clear logo
            var game = PluginHelper.StateManager.GetAllSelectedGames()?.FirstOrDefault();
            if (game != null && !string.IsNullOrEmpty(game.ClearLogoImagePath))
            {
                SendGame(game);
                return;
            }

            // 2. Fallback: selected platform's clear logo. A platform has no
            //    marquee of its own, so the proxy falls back to the logo.
            var platform = PluginHelper.StateManager.GetSelectedPlatform();
            if (platform != null && !string.IsNullOrEmpty(platform.ClearLogoImagePath))
            {
                Send(platform.Name, platform.Name, platform.ClearLogoImagePath, "");
                return;
            }

            // 3. Nothing selected => default
            Send("", "", "", "");
        }

        // ── ISystemEventsPlugin ──────────────────────────────────────────
        public void OnEventRaised(string eventType)
        {
            if (eventType == SystemEventTypes.SelectionChanged)
                UpdateDisplay();
        }

        // ── IGameLaunchingPlugin ──────────────────────────────────────────
        public void OnBeforeGameLaunching(IGame game, IAdditionalApplication app, IEmulator emulator)
            => SendGame(game);

        public void OnAfterGameLaunched(IGame game, IAdditionalApplication app, IEmulator emulator)
            => SendGame(game);

        public void OnGameExited()
            => UpdateDisplay();
    }
}
