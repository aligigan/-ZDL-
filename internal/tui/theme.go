package tui

import "github.com/charmbracelet/lipgloss"

// themeMode is "auto", "dark", or "light".
var themeMode = "auto"

var (
	colAccent = lipgloss.AdaptiveColor{Light: "126", Dark: "212"}
	colCyan   = lipgloss.AdaptiveColor{Light: "30", Dark: "81"}
	colGreen  = lipgloss.AdaptiveColor{Light: "28", Dark: "78"}
	colAmber  = lipgloss.AdaptiveColor{Light: "130", Dark: "214"}
	colRed    = lipgloss.AdaptiveColor{Light: "160", Dark: "203"}
	colMuted  = lipgloss.AdaptiveColor{Light: "245", Dark: "244"}
	colFaint  = lipgloss.AdaptiveColor{Light: "250", Dark: "238"}
	colSelBg  = lipgloss.AdaptiveColor{Light: "254", Dark: "237"}
)

// fixedColor returns a lipgloss.TerminalColor for the given light/dark pair
// based on the current theme mode.
func fixedColor(light, dark string) lipgloss.TerminalColor {
	switch themeMode {
	case "dark":
		return lipgloss.Color(dark)
	case "light":
		return lipgloss.Color(light)
	default: // auto
		return lipgloss.AdaptiveColor{Light: light, Dark: dark}
	}
}

var (
	stLogo   = lipgloss.NewStyle().Bold(true).Foreground(fixedColor("126", "212"))
	stTitle  = lipgloss.NewStyle().Bold(true).Foreground(fixedColor("30", "81"))
	stDim    = lipgloss.NewStyle().Foreground(fixedColor("245", "244"))
	stFaint  = lipgloss.NewStyle().Foreground(fixedColor("250", "238"))
	stError  = lipgloss.NewStyle().Foreground(fixedColor("160", "203"))
	stOK     = lipgloss.NewStyle().Foreground(fixedColor("28", "78"))
	stWarn   = lipgloss.NewStyle().Foreground(fixedColor("130", "214"))
	stActive = lipgloss.NewStyle().Foreground(fixedColor("30", "81"))
	stSpeed  = lipgloss.NewStyle().Foreground(fixedColor("28", "78"))
	stSel    = lipgloss.NewStyle().Background(fixedColor("254", "237"))
	stRule   = lipgloss.NewStyle().Foreground(fixedColor("250", "238"))
	stKey    = lipgloss.NewStyle().Foreground(fixedColor("126", "212")).Bold(true)
	stBox    = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(fixedColor("126", "212")).
			Padding(0, 1)
)

// SetThemeMode switches the UI theme to "auto", "dark", or "light".
func SetThemeMode(mode string) {
	switch mode {
	case "auto", "dark", "light":
		themeMode = mode
	default:
		themeMode = "auto"
	}
	stLogo = lipgloss.NewStyle().Bold(true).Foreground(fixedColor("126", "212"))
	stTitle = lipgloss.NewStyle().Bold(true).Foreground(fixedColor("30", "81"))
	stDim = lipgloss.NewStyle().Foreground(fixedColor("245", "244"))
	stFaint = lipgloss.NewStyle().Foreground(fixedColor("250", "238"))
	stError = lipgloss.NewStyle().Foreground(fixedColor("160", "203"))
	stOK = lipgloss.NewStyle().Foreground(fixedColor("28", "78"))
	stWarn = lipgloss.NewStyle().Foreground(fixedColor("130", "214"))
	stActive = lipgloss.NewStyle().Foreground(fixedColor("30", "81"))
	stSpeed = lipgloss.NewStyle().Foreground(fixedColor("28", "78"))
	stSel = lipgloss.NewStyle().Background(fixedColor("254", "237"))
	stRule = lipgloss.NewStyle().Foreground(fixedColor("250", "238"))
	stKey = lipgloss.NewStyle().Foreground(fixedColor("126", "212")).Bold(true)
	stBox = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(fixedColor("126", "212")).
		Padding(0, 1)
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func spinner(tick int) string {
	return spinnerFrames[tick%len(spinnerFrames)]
}
