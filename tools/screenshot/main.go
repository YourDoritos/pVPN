// Command screenshot renders pVPN's terminal screens to SVG.
//
// Generated rather than hand-captured so the images in the README cannot
// drift from the code: rerun it after a UI change and the screenshots are
// correct again. It draws the same models the running program does, through
// the same exported entry points.
//
// The output is deliberately identical in style to pDrive's, so the two
// projects read as one family.
//
//	go run ./tools/screenshot -out assets
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/YourDoritos/pvpn/internal/api"
	"github.com/YourDoritos/pvpn/internal/config"
	"github.com/YourDoritos/pvpn/internal/tui"
	"github.com/YourDoritos/pvpn/internal/vpn"
)

const (
	screenWidth  = frameCols
	screenHeight = frameRows
	// navHeight is the tab bar: border-top, labels, border-bottom.
	navHeight = 3
)

type shot struct {
	name    string
	title   string
	caption string
	body    string
	align   vAlign
}

func main() {
	out := flag.String("out", "assets", "directory to write the SVGs into")
	flag.Parse()

	// Colour is chosen by the terminal profile, and there is no terminal
	// here. Without this every screenshot renders in plain grey.
	lipgloss.SetColorProfile(termenv.TrueColor)

	if err := os.MkdirAll(*out, 0755); err != nil {
		fail(err)
	}

	for _, s := range shots() {
		svg := renderSVG(s.title, parseANSI(s.body), s.align)
		path := filepath.Join(*out, s.name+".svg")
		if err := os.WriteFile(path, []byte(svg), 0644); err != nil {
			fail(err)
		}
		fmt.Printf("  %-24s %s\n", s.name+".svg", s.caption)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "screenshot:", err)
	os.Exit(1)
}

// demoConfig is the configuration the screenshots are taken against.
func demoConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.Connection.Protocol = "stealth"
	cfg.Connection.KillSwitch = true
	cfg.Features.NetShield = 2
	cfg.Features.VPNAccelerator = true
	cfg.Features.PortForwarding = true
	return cfg
}

func shots() []shot {
	cfg := demoConfig()
	now := time.Now()

	vpnInfo := &api.VPNInfoResponse{VPN: api.VPNInfo{
		Name: "you", PlanName: "vpn2022", PlanTitle: "Proton Unlimited", Status: 1,
		// The grid filters by what the plan can reach; without a tier the
		// demo servers are all filtered out and the screen renders empty.
		MaxTier: 2, MaxConnect: 10,
	}}

	// --- Status ---
	status := tui.NewStatusModel()
	status.SetSize(screenWidth, screenHeight)
	status.SetConnected(vpn.ConnectionInfo{
		ServerName:     "DE#905",
		ServerIP:       "194.126.177.218",
		ServerCountry:  "DE",
		ConnectedAt:    now.Add(-83*time.Minute - 4*time.Second),
		ForwardedPort:  51423,
		ForwardedProto: "TCP+UDP",
	})
	status.SetDaemonStats(2_255_477_146, 134_642_074,
		now.Add(-96*time.Second).Unix())

	// --- Servers ---
	servers := tui.NewServersModel(vpnInfo)
	// The grid fills whatever height it is given, so it gets the body budget
	// (the frame less the tab bar and the blank row top and bottom) rather
	// than the whole frame, which would overflow the window.
	servers.SetSize(screenWidth, screenHeight-navHeight-2)
	servers.SetServers(demoServers())
	servers.SetVPNState("connected")
	servers.SetConnectedServer("DE#905")

	// --- Settings ---
	settings := tui.NewSettingsModel()
	settings.SetSize(screenWidth, screenHeight)
	settings.SetAccountInfo("you@proton.me", "Proton Unlimited")

	// --- Login ---
	login := tui.NewLoginModel()
	login.SetSize(screenWidth, screenHeight)

	return []shot{
		{"tui-status", "pvpn — status", "live server, country, uptime, traffic",
			withNav(tui.ViewStatus, status.View()), alignTop},
		{"tui-servers", "pvpn — servers", "every server, filtered and searchable",
			withNav(tui.ViewServers, servers.View()), alignTop},
		{"tui-settings", "pvpn — settings", "every feature toggleable in place",
			withNav(tui.ViewSettings, settings.ViewWithConfig(cfg)), alignTop},
		// No tab bar above the login form, so nothing to top-align it with.
		{"tui-login", "pvpn — sign in", "Proton SRP with 2FA on first launch",
			login.View(), alignMiddle},
	}
}

// withNav stacks the tab bar above a screen, exactly as the running program
// does.
func withNav(active tui.View, body string) string {
	return lipgloss.JoinVertical(lipgloss.Left,
		tui.RenderNav(screenWidth, active, ""), body)
}

// demoServers builds the server list the Servers tab is rendered against.
//
// The real list is ~17,000 logical servers; reproducing that here would only
// slow the tool down, since the grid shows one card per country with a count.
// These are the counts as they actually are, so the screenshot does not claim
// a network Proton does not have.
func demoServers() []api.LogicalServer {
	type country struct {
		code     string
		city     string
		count    int
		features int
	}
	countries := []country{
		{"US", "New York", 5992, api.ServerFeatureP2P | api.ServerFeatureStreaming},
		{"DE", "Berlin", 714, api.ServerFeatureP2P | api.ServerFeatureStreaming},
		{"UK", "London", 775, api.ServerFeatureStreaming},
		{"FR", "Paris", 601, api.ServerFeatureP2P},
		{"NL", "Amsterdam", 412, api.ServerFeatureP2P | api.ServerFeatureStreaming},
		{"JP", "Tokyo", 356, api.ServerFeatureStreaming},
		{"ES", "Madrid", 293, api.ServerFeatureP2P},
		{"SG", "Singapore", 242, api.ServerFeatureStreaming},
		{"CA", "Toronto", 231, api.ServerFeatureP2P | api.ServerFeatureStreaming},
		{"IT", "Milan", 208, api.ServerFeatureP2P},
		{"AU", "Sydney", 176, api.ServerFeatureStreaming},
		{"CH", "Zurich", 145, api.ServerFeatureSecureCore | api.ServerFeatureP2P},
		{"SE", "Stockholm", 132, api.ServerFeatureSecureCore},
		{"IS", "Reykjavik", 118, api.ServerFeatureSecureCore},
		{"PL", "Warsaw", 110, api.ServerFeatureP2P},
		{"BR", "Sao Paulo", 99, api.ServerFeatureStreaming},
		{"IN", "Mumbai", 97, api.ServerFeatureStreaming},
		{"FI", "Helsinki", 96, api.ServerFeatureP2P},
		{"NO", "Oslo", 88, api.ServerFeatureP2P},
		{"AT", "Vienna", 71, api.ServerFeatureP2P},
		{"BE", "Brussels", 65, api.ServerFeatureP2P},
		{"IE", "Dublin", 62, api.ServerFeatureStreaming},
		{"DK", "Copenhagen", 59, api.ServerFeatureP2P},
		{"CZ", "Prague", 53, api.ServerFeatureP2P},
		{"RO", "Bucharest", 52, api.ServerFeatureTor},
		{"PT", "Lisbon", 49, api.ServerFeatureP2P},
		{"HK", "Hong Kong", 42, api.ServerFeatureStreaming},
		{"ZA", "Johannesburg", 39, api.ServerFeatureStreaming},
		{"MX", "Mexico City", 33, api.ServerFeatureP2P},
		{"KR", "Seoul", 29, api.ServerFeatureStreaming},
		{"AR", "Buenos Aires", 24, api.ServerFeatureP2P},
		{"NZ", "Auckland", 21, api.ServerFeatureStreaming},
	}

	var out []api.LogicalServer
	for _, c := range countries {
		for i := 1; i <= c.count; i++ {
			out = append(out, api.LogicalServer{
				ID:          fmt.Sprintf("%s-%d", c.code, i),
				Name:        fmt.Sprintf("%s#%d", c.code, i),
				ExitCountry: c.code,
				City:        c.city,
				Tier:        2,
				Status:      1,
				Features:    c.features,
				// A spread of loads, so the bars in the list are not all
				// the same length.
				Load: 12 + (i*7)%78,
			})
		}
	}
	return out
}
