package access

import _ "embed"

// themeScript applies the owner's Vult theme on sign-in pages before the first
// paint and runs the theme menu in their top bar.
//
//go:embed theme.js
var themeScript string

// authHead loads the sign-in styles and the theme they are drawn in.
const authHead = `<link rel="stylesheet" href="/auth/style.css"><script src="/auth/theme.js"></script><script src="/auth/ui.js" defer></script>`

const (
	sunIcon     = "M116,36V20a12,12,0,0,1,24,0V36a12,12,0,0,1-24,0Zm80,92a68,68,0,1,1-68-68A68.07,68.07,0,0,1,196,128Zm-24,0a44,44,0,1,0-44,44A44.05,44.05,0,0,0,172,128ZM51.51,68.49a12,12,0,1,0,17-17l-12-12a12,12,0,0,0-17,17Zm0,119-12,12a12,12,0,0,0,17,17l12-12a12,12,0,1,0-17-17ZM196,72a12,12,0,0,0,8.49-3.51l12-12a12,12,0,0,0-17-17l-12,12A12,12,0,0,0,196,72Zm8.49,115.51a12,12,0,0,0-17,17l12,12a12,12,0,0,0,17-17ZM48,128a12,12,0,0,0-12-12H20a12,12,0,0,0,0,24H36A12,12,0,0,0,48,128Zm80,80a12,12,0,0,0-12,12v16a12,12,0,0,0,24,0V220A12,12,0,0,0,128,208Zm108-92H220a12,12,0,0,0,0,24h16a12,12,0,0,0,0-24Z"
	moonIcon    = "M236.37,139.4a12,12,0,0,0-12-3A84.07,84.07,0,0,1,119.6,31.59a12,12,0,0,0-15-15A108.86,108.86,0,0,0,49.69,55.07,108,108,0,0,0,136,228a107.09,107.09,0,0,0,64.93-21.69,108.86,108.86,0,0,0,38.44-54.94A12,12,0,0,0,236.37,139.4Zm-49.88,47.74A84,84,0,0,1,68.86,69.51,84.93,84.93,0,0,1,92.27,48.29Q92,52.13,92,56A108.12,108.12,0,0,0,200,164q3.87,0,7.71-.27A84.79,84.79,0,0,1,186.49,187.14Z"
	monitorIcon = "M208,36H48A28,28,0,0,0,20,64V176a28,28,0,0,0,28,28H208a28,28,0,0,0,28-28V64A28,28,0,0,0,208,36Zm4,140a4,4,0,0,1-4,4H48a4,4,0,0,1-4-4V64a4,4,0,0,1,4-4H208a4,4,0,0,1,4,4Zm-40,52a12,12,0,0,1-12,12H96a12,12,0,0,1,0-24h64A12,12,0,0,1,172,228Z"
	checkIcon   = "M232.49,80.49l-128,128a12,12,0,0,1-17,0l-56-56a12,12,0,1,1,17-17L96,183,215.51,63.51a12,12,0,0,1,17,17Z"
)

func themeIcon(class, path string) string {
	return `<svg class="` + class + `" aria-hidden="true" viewBox="0 0 256 256" fill="currentColor"><path d="` + path + `"/></svg>`
}

func themeChoice(id, label, path string) string {
	return `<button type="button" role="menuitemradio" aria-checked="false" data-theme-choice="` + id + `">` + themeIcon("icon", path) + label + themeIcon("check", checkIcon) + `</button>`
}

// authBar is the top bar every sign-in page shares with the first-run scene:
// the quiet logo, and a theme menu once its script runs.
var authBar = `<header class="bar"><span class="mark">` + vultLockup + `</span><div class="theme" id="theme" hidden>` +
	`<button type="button" id="theme-button" class="theme-button" aria-haspopup="menu" aria-expanded="false" aria-controls="theme-menu" aria-label="Theme">` +
	themeIcon("icon when-light", sunIcon) + themeIcon("icon when-dark", moonIcon) + themeIcon("icon when-system", monitorIcon) + `</button>` +
	`<div id="theme-menu" class="theme-menu" role="menu" aria-label="Theme" hidden>` +
	themeChoice("light", "Light", sunIcon) + themeChoice("dark", "Dark", moonIcon) + themeChoice("system", "System", monitorIcon) +
	`</div></div></header>`

// fingerprintIcon and checkMarkIcon draw the passkey dial and its button.
func fingerprintIcon(class string) string {
	return `<svg class="stroke ` + class + `" aria-hidden="true" viewBox="0 0 24 24"><path d="M6.918 3.386A9.954 9.954 0 0 1 12 2c5.523 0 10 4.477 10 10v.1M3.38 6.925A9.954 9.954 0 0 0 2 12c0 4.66 3.187 8.575 7.5 9.685m11.208-4.764a10.02 10.02 0 0 1-6.208 4.764m-.41-15.311A6 6 0 0 0 6.377 14.1m11.247-4.192a6 6 0 0 1-7.701 7.723M12 10v4"/></svg>`
}

func checkMarkIcon(class string) string {
	return `<svg class="stroke ` + class + `" aria-hidden="true" viewBox="0 0 24 24"><path d="M20 6 9 17l-5-5"/></svg>`
}

func copyIcon(class string) string {
	return `<svg class="stroke ` + class + `" aria-hidden="true" viewBox="0 0 24 24"><path d="M5 15c-.932 0-1.398 0-1.765-.152a2 2 0 0 1-1.083-1.083C2 13.398 2 12.932 2 12V5.2c0-1.12 0-1.68.218-2.108a2 2 0 0 1 .874-.874C3.52 2 4.08 2 5.2 2H12c.932 0 1.398 0 1.765.152a2 2 0 0 1 1.083 1.083C15 3.602 15 4.068 15 5m-2.8 17h6.6c1.12 0 1.68 0 2.108-.218a2 2 0 0 0 .874-.874C22 20.48 22 19.92 22 18.8v-6.6c0-1.12 0-1.68-.218-2.108a2 2 0 0 0-.874-.874C20.48 9 19.92 9 18.8 9h-6.6c-1.12 0-1.68 0-2.108.218a2 2 0 0 0-.874.874C9 10.52 9 11.08 9 12.2v6.6c0 1.12 0 1.68.218 2.108a2 2 0 0 0 .874.874C10.52 22 11.08 22 12.2 22Z"/></svg>`
}

func downloadIcon() string {
	return `<svg class="stroke" aria-hidden="true" viewBox="0 0 24 24"><path d="M21 15v1.2c0 1.68 0 2.52-.327 3.162a3 3 0 0 1-1.311 1.311C18.72 21 17.88 21 16.2 21H7.8c-1.68 0-2.52 0-3.162-.327a3 3 0 0 1-1.311-1.311C3 18.72 3 17.88 3 16.2V15m14-5-5 5m0 0-5-5m5 5V3"/></svg>`
}
