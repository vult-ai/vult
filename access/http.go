package access

import (
	"crypto/subtle"
	"encoding/json"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/vult-ai/vult/internal/loopback"
	"github.com/vult-ai/vult/internal/operationref"
)

const SessionCookie = "__Host-vult-session"
const loginCookie = "__Host-vult-login"
const attemptCookie = "__Host-vult-attempt"
const reviewCookie = "__Host-vult-review"
const LoginCSS = `/* Vult's own sign-in and account pages, matched to the managed sign-in: one 380px column on the canvas, the logo lockup, a page title, then controls. */ :root{color-scheme:light dark;--bg:#fafafa;--fg:#0a0a0a;--muted:#525252;--faint:#737373;--line:#e5e5e5;--card:#fff;--button:#0a0a0a;--button-fg:#fafafa;--secondary:#fff;font-family:Geist,"Geist Variable",system-ui,-apple-system,sans-serif;font-feature-settings:"ss01"} @media (prefers-color-scheme:dark){:root:not(.light-mode){--bg:#0a0a0a;--fg:#fafafa;--muted:#a3a3a3;--faint:#8a8a8a;--line:#262626;--card:#141414;--button:#e5e5e5;--button-fg:#0a0a0a;--secondary:#141414}} :root.dark-mode{--bg:#0a0a0a;--fg:#fafafa;--muted:#a3a3a3;--faint:#8a8a8a;--line:#262626;--card:#141414;--button:#e5e5e5;--button-fg:#0a0a0a;--secondary:#141414} :root.light-mode{color-scheme:light}:root.dark-mode{color-scheme:dark} *{box-sizing:border-box}[hidden]{display:none!important} body{margin:0;min-height:100dvh;display:flex;background:var(--bg);color:var(--fg);-webkit-font-smoothing:antialiased;isolation:isolate} .particles{position:fixed;inset:0;z-index:-1;pointer-events:none} @media (prefers-color-scheme:dark){:root:not(.light-mode) .particles{filter:brightness(1.8)}} .dark-mode .particles{filter:brightness(1.8)} main{width:min(100% - 48px,380px);margin:auto;padding-block:112px;text-align:start} main>*{animation:rise .5s cubic-bezier(.2,.7,.2,1) both}main>*:nth-child(2){animation-delay:40ms}main>*:nth-child(3){animation-delay:80ms}main>*:nth-child(n+4){animation-delay:120ms} @keyframes rise{from{opacity:0;transform:translateY(6px)}}@media (prefers-reduced-motion:reduce){main>*{animation:none}} .brand{display:block;height:20px;width:auto;color:var(--fg);margin:0 0 24px} h1{font-size:32px;line-height:1.15;font-weight:600;letter-spacing:-.025em;margin:0 0 8px;text-wrap:balance} h1+p{font-size:16px;color:var(--muted);margin:0 0 28px} h2{font-size:15px;font-weight:600;margin:28px 0 4px} p{font-size:15px;line-height:1.55;color:var(--muted);margin:0 0 16px;text-wrap:pretty} .fine{font-size:13px;color:var(--faint);margin-top:28px}.start{text-align:start} .qr{display:grid;place-items:center;margin:16px 0;width:200px;padding:12px;background:#fff;border-radius:12px}.qr svg{width:100%;height:auto;display:block} a{color:var(--fg);text-underline-offset:3px;text-decoration-color:var(--faint)} code{font-family:"Geist Mono",ui-monospace,Menlo,monospace;font-size:14px} label{display:block;font-size:14px;font-weight:500;margin-top:20px;color:var(--fg)} input:not([type=checkbox]):not([type=radio]),select{width:100%;height:42px;padding:0 12px;font:inherit;font-size:15px;color:inherit;background:var(--card);border:1px solid var(--line);border-radius:10px;margin:8px 0 12px;box-shadow:0 1px 2px rgba(0,0,0,.04)} input:focus,select:focus{outline:none;border-color:var(--fg)} fieldset{border:0;padding:0;margin:20px 0 8px}legend{font-size:14px;font-weight:600;margin-bottom:4px} fieldset label{display:flex;gap:10px;align-items:flex-start;margin-top:10px;color:var(--muted);font-weight:400} button,.button{display:flex;align-items:center;justify-content:center;width:100%;height:42px;border:1px solid var(--button);border-radius:10px;background:var(--button);color:var(--button-fg);padding:0 16px;font:inherit;font-size:15px;font-weight:500;cursor:pointer;text-decoration:none;margin-top:10px;transition:opacity .15s} button:hover,.button:hover{opacity:.9} button[value=deny],button.secondary,.button.secondary{background:var(--secondary);color:var(--fg);border-color:var(--line)} button:disabled,.button[aria-disabled=true]{opacity:.45;cursor:default;pointer-events:none} :focus-visible{outline:2px solid var(--fg);outline-offset:3px} [role=status]{min-height:22px;font-size:14px;color:var(--faint);margin-top:12px} .codes{list-style:none;padding:0;margin:20px 0 8px;display:grid;grid-template-columns:minmax(0,1fr);gap:6px;counter-reset:code} .codes li{display:flex;gap:12px;align-items:baseline;padding:9px 12px;overflow-wrap:anywhere;border:1px solid var(--line);border-radius:10px;background:var(--card);counter-increment:code} .codes li::before{content:counter(code,decimal-leading-zero);font:12px "Geist Mono",ui-monospace,Menlo,monospace;color:var(--faint)} .key{padding:12px;border:1px solid var(--line);border-radius:10px;background:var(--card);overflow-wrap:anywhere} .row{display:flex;gap:8px;margin:12px 0}.row .button{margin:0} .check{display:flex;gap:10px;align-items:center;margin:20px 0 4px;color:var(--fg)} .bar{position:absolute;inset:28px 32px auto;z-index:2;display:flex;align-items:center;justify-content:space-between;max-width:1120px;margin-inline:auto} .mark{display:flex;opacity:.5;filter:grayscale(1)}.mark .brand{margin:0} .theme{position:relative} .bar button{width:auto;height:auto;margin:0;border:0;background:none;color:var(--fg);font-size:14px;font-weight:400} .bar .theme-button{display:grid;place-items:center;width:36px;height:36px;border-radius:8px;color:var(--muted)} .bar .theme-button:hover,.bar .theme-button[aria-expanded=true]{background:var(--line);opacity:1} .bar .icon{width:20px;height:20px;flex:none} .theme-button .icon{display:none} [data-vult-theme-preference=light] .when-light,[data-vult-theme-preference=dark] .when-dark,[data-vult-theme-preference=system] .when-system{display:block} .theme-menu{position:absolute;top:calc(100% + 8px);right:0;min-width:max-content;padding:4px;background:var(--card);border:1px solid var(--line);border-radius:10px;box-shadow:0 8px 24px rgba(0,0,0,.12)} .bar .theme-menu button{display:flex;align-items:center;justify-content:flex-start;gap:8px;width:100%;height:34px;padding:0 10px;border-radius:6px} .bar .theme-menu button:hover,.bar .theme-menu button:focus-visible{background:var(--line);opacity:1;outline:none} .theme-menu .icon{width:16px;height:16px;color:var(--muted)} .theme-menu .check{width:14px;height:14px;margin-left:auto;padding-left:12px;box-sizing:content-box;visibility:hidden} .theme-menu [aria-checked=true] .check{visibility:visible} .threshold{display:flex;flex-direction:column;align-items:center;gap:14px;text-align:center} .threshold h1,.threshold p{margin:0}.threshold .lede{max-width:38ch;margin:0 0 8px;font-size:16px;color:var(--muted)} .threshold .fine{margin:0;white-space:nowrap}.threshold label{width:100%;text-align:start} .dial{position:relative;width:200px;height:200px;margin-bottom:12px;color:var(--muted)} .dial>svg{position:absolute;inset:0;width:100%;height:100%;overflow:visible;fill:none} .dial circle{stroke:var(--line);stroke-width:1}.dial .ticks{stroke:var(--faint);stroke-width:2;stroke-dasharray:1 15.27} .dial .dash{stroke:var(--faint);stroke-opacity:.6;stroke-dasharray:36 12}.dial .dots{stroke:var(--faint);stroke-dasharray:2 7;opacity:.6} .ring-outer,.ring-inner{transform-origin:114px 114px;transition:transform 1.1s cubic-bezier(.2,.7,.2,1),opacity .8s ease} .ring-outer{animation:dial-spin 60s linear infinite}.ring-inner{animation:dial-spin 90s linear infinite reverse} .core{position:absolute;inset:56px;display:grid;place-items:center;border-radius:999px;background:var(--bg);box-shadow:0 12px 32px rgba(0,0,0,.18),0 0 0 1px var(--line);transition:transform .8s cubic-bezier(.2,.7,.2,1),box-shadow .5s ease} .stroke{fill:none;stroke:currentColor;stroke-width:2;stroke-linecap:round;stroke-linejoin:round} .core .stroke{width:44px;height:44px}.core .when-open{display:none} .dial[data-phase=reading] .core{box-shadow:0 12px 32px rgba(0,0,0,.18),0 0 0 2px var(--faint)}.dial[data-phase=reading] .core .stroke{animation:dial-scan 1s ease-in-out infinite} .dial[data-phase=reading] .ring-outer{animation-duration:8s}.dial[data-phase=reading] .ring-inner{animation-duration:12s} .dial[data-phase=open] .ring-outer{animation:none;transform:scale(1.8);opacity:0}.dial[data-phase=open] .ring-inner{animation:none;transform:scale(2.4);opacity:0} .dial[data-phase=open] .core{color:#17b26a;transform:scale(.9)}.dial[data-phase=open] .when-closed{display:none}.dial[data-phase=open] .when-open{display:block} @keyframes dial-spin{to{transform:rotate(360deg)}}@keyframes dial-scan{50%{opacity:.4}} @media (prefers-reduced-motion:reduce){.ring-outer,.ring-inner,.core,.core .stroke{animation:none;transition:none}} .threshold button.primary{width:auto;gap:8px;padding:0 18px;margin:0}.threshold button.primary .stroke{width:20px;height:20px} .threshold button.link{width:auto;height:auto;margin:0;padding:4px 2px;border:0;background:none;color:var(--fg);font-size:15px;font-weight:500} .threshold button.link:hover{opacity:.75} .threshold button.wide{width:auto;min-width:220px;margin:0} .threshold-status{margin:-4px 0 0;color:var(--fg)}.threshold-status:empty{min-height:0;margin-top:-14px}.threshold-help{display:grid;justify-items:center;gap:10px;max-width:40ch;font-size:14px} .recovery h1+p{margin-bottom:24px}.code-actions{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:12px} .code-actions>*{gap:8px;margin:0}.code-actions .stroke{width:18px;height:18px}.code-actions .when-copied,.code-actions [data-copied] .when-ready{display:none}.code-actions [data-copied] .when-copied{display:block} .recovery .codes{gap:10px;margin:16px 0;padding:18px 20px;border:1px solid var(--line);border-radius:10px;background:var(--card)} .recovery .codes li{padding:0;border:0;border-radius:0;background:none;opacity:0;transform:translateY(4px);animation:code-rise .4s ease-out forwards} .recovery .codes li:nth-child(2){animation-delay:50ms}.recovery .codes li:nth-child(3){animation-delay:100ms}.recovery .codes li:nth-child(4){animation-delay:150ms}.recovery .codes li:nth-child(5){animation-delay:200ms}.recovery .codes li:nth-child(6){animation-delay:250ms}.recovery .codes li:nth-child(7){animation-delay:300ms}.recovery .codes li:nth-child(8){animation-delay:350ms} .recovery .codes code{font-size:13px;letter-spacing:.02em}@keyframes code-rise{to{opacity:1;transform:none}} @media (prefers-reduced-motion:reduce){.recovery .codes li{opacity:1;transform:none;animation:none}} .recovery h2{margin:24px 0 4px}.recovery .fine{margin:0 0 8px}.saved-note{margin:4px 0 0}.sr-only{position:absolute;width:1px;height:1px;overflow:hidden;clip-path:inset(50%);white-space:nowrap;min-height:0;margin:0} .enroll h1{margin:0 0 20px}.steps{display:grid;gap:6px;margin:0 0 16px;padding-inline-start:20px;color:var(--muted);font-size:15px;line-height:22px}.steps>li{padding-inline-start:4px} .enroll-panel{display:grid;gap:12px;padding:12px 16px 16px;border:1px solid var(--line);border-radius:12px;background:var(--card)} .manual{display:flex;align-items:center;justify-content:center;gap:8px;min-width:0} .manual code{min-width:0;overflow-wrap:anywhere;color:var(--muted);font-size:13px;line-height:18px;user-select:all} .enroll .icon-button{display:grid;place-items:center;flex:none;width:32px;height:32px;margin:0;padding:0;border:0;border-radius:8px;background:none;color:var(--muted)} .enroll .icon-button:hover{background:var(--line);opacity:1}.icon-button .stroke{width:16px;height:16px} .icon-button .when-copied,.icon-button[data-copied] .when-ready{display:none}.icon-button[data-copied] .when-copied{display:block} .enroll .qr{width:184px;margin:0 auto;padding:10px} .otp-heading{display:flex;min-height:20px;align-items:center;justify-content:space-between;gap:12px;margin-top:20px}.otp-heading label{margin:0} .verifying{color:var(--muted);font-size:13px} .otp{position:relative;margin:8px 0 12px} .otp input{position:absolute!important;inset:0;width:100%!important;height:100%!important;margin:0!important;padding:0!important;opacity:0;border:0;font-size:16px;caret-color:transparent;cursor:text} .slots{display:flex;gap:4px;pointer-events:none} .slot{display:flex;flex:1;min-width:0;height:48px;align-items:center;justify-content:center;border:1px solid var(--line);border-radius:10px;background:var(--card);font-size:20px;font-weight:500;font-variant-numeric:tabular-nums;color:var(--fg)} .slot[data-separator]{margin-inline-end:4px}.slot:empty::before{content:"·";color:var(--faint);opacity:.4} .slot[data-active]{border-color:var(--fg)}.slot[data-active]:empty::before{content:"";width:1px;height:20px;background:var(--fg);opacity:1;animation:caret 1s step-end infinite} .otp[data-invalid] .slot{border-color:#f04438}@keyframes caret{50%{opacity:0}} .enroll button#authenticator-verify{margin-top:4px} .enroll button.link{display:flex;width:auto;height:auto;margin:16px auto 0;padding:4px 2px;border:0;background:none;color:var(--fg);font-size:15px;font-weight:500}.enroll button.link:hover{opacity:.75} @media (max-width:420px){main{padding-block:72px}.bar{inset:20px 16px auto}.threshold .fine{white-space:normal}}`

// vultLockup is the Vult logo, inline so sign-in pages need no other request.
const vultLockup = `<svg class="brand" role="img" aria-label="Vult" xmlns="http://www.w3.org/2000/svg" viewBox="0 -800 3299 970" fill="none"><g transform="translate(0 -780) scale(26.31578947368421)"><path d="M16.6198 30.9912C17.4308 30.5236 17.9309 29.5191 17.3107 28.4468L3.5193 4.55842C2.90051 3.48613 1.78014 3.4169 0.969217 3.88594C0.158291 4.35356 -0.341813 5.35804 0.276977 6.43033L14.0684 30.3187C14.6872 31.391 15.8075 31.4603 16.6184 30.9912H16.6198Z" fill="#007DF9"/><path d="M35.1928 3.27889C34.2985 3.79455 33.992 4.94031 34.5076 5.83459C35.0233 6.73028 36.169 7.03683 37.0647 6.51976C37.959 6.0041 38.2656 4.85837 37.7499 3.96409C37.2342 3.06981 36.0885 2.76182 35.1942 3.27889H35.1928Z" fill="#007DF9"/><path d="M17.5551 32.6116C16.6609 33.1287 16.3543 34.273 16.8699 35.1687C17.387 36.0644 18.5314 36.3696 19.427 35.8539C20.3213 35.3383 20.6293 34.1925 20.1122 33.2968C19.5951 32.4025 18.4508 32.096 17.5565 32.6116H17.5551Z" fill="#007DF9"/><path d="M28.9216 11.1284L30.8345 14.4413C31.4533 15.5136 32.5736 15.5828 33.3846 15.1152C34.1941 14.6476 34.6956 13.6431 34.0754 12.5708L28.4596 2.84394C27.8408 1.77165 26.7205 1.70242 25.911 2.17146C25.1001 2.63909 24.6 3.64356 25.2188 4.71585L26.4564 6.86041C27.6629 8.98662 24.4347 10.8444 23.207 8.71677L19.8164 2.84532C19.1976 1.77303 18.0773 1.70384 17.2678 2.17288C16.4568 2.6405 15.9567 3.64494 16.5755 4.71723C17.7904 6.84626 14.5581 8.70831 13.3276 6.57928L11.1716 2.84673C10.5528 1.77445 9.43259 1.70522 8.62308 2.17426C7.81215 2.64188 7.31198 3.64636 7.93077 4.71864L22.1898 29.4166C22.8086 30.4889 23.9289 30.5581 24.7398 30.089C25.5507 29.6214 26.0509 28.6169 25.4321 27.5447L15.8069 10.8755C14.5778 8.74785 17.8074 6.87876 19.0422 8.99366L26.5114 21.9303C27.1316 23.0026 28.2506 23.0719 29.0615 22.6028C29.871 22.1352 30.3725 21.1307 29.7537 20.0584L25.6864 13.013C24.4573 10.8854 27.6869 9.01629 28.9216 11.1326V11.1284Z" fill="#007DF9"/></g><path transform="translate(1300 0)" d="M579 -500Q594 -500 594 -485V-455Q594 -440 579 -440H564Q548 -440 535.5 -428Q523 -416 506 -378L337 -11Q332 0 322 0H265Q254 0 249 -11L81 -377Q64 -415 53.5 -427.5Q43 -440 23 -440H21Q6 -440 6 -455V-485Q6 -500 21 -500H252Q267 -500 267 -485V-455Q267 -440 252 -440H250Q231 -440 223.5 -432.5Q216 -425 216 -411Q216 -398 221.5 -382Q227 -366 237 -345L329 -150L413 -345Q423 -368 430 -387Q437 -406 437 -419Q437 -440 415 -440H395Q380 -440 380 -455V-485Q380 -500 395 -500ZM1192 -202Q1192 -176 1195.5 -150.5Q1199 -125 1211.5 -107.5Q1224 -90 1251 -87Q1259 -86 1260.5 -82.5Q1262 -79 1262 -74V-41Q1262 -27 1248 -27Q1199 -27 1166.5 -21.5Q1134 -16 1110.5 -7.5Q1087 1 1064 11Q1060 13 1056 13H1054Q1052 13 1052 10Q1052 7 1055.5 -20.5Q1059 -48 1068 -116Q1054 -88 1029 -58Q1004 -28 968.5 -7.5Q933 13 887 13Q833 13 799.5 -3.5Q766 -20 748 -47.5Q730 -75 723.5 -109.5Q717 -144 717 -180V-345Q717 -386 698.5 -413Q680 -440 643 -440H639Q624 -440 624 -455V-485Q624 -500 639 -500H842Q857 -500 857 -485V-174Q857 -147 862 -124.5Q867 -102 882.5 -88.5Q898 -75 929 -75Q954 -75 979 -87.5Q1004 -100 1023.5 -119.5Q1043 -139 1051 -160V-345Q1051 -386 1032.5 -413Q1014 -440 978 -440H976Q961 -440 961 -455V-485Q961 -500 976 -500H1177Q1192 -500 1192 -485ZM1604 -60Q1619 -60 1619 -45V-15Q1619 0 1602 0H1319Q1302 0 1302 -15V-45Q1302 -60 1317 -60H1319Q1356 -60 1373.5 -87.5Q1391 -115 1391 -155V-530Q1391 -558 1387.5 -583Q1384 -608 1372 -625.5Q1360 -643 1332 -646Q1320 -647 1320 -659V-693Q1320 -699 1324 -701Q1328 -703 1335 -706Q1406 -732 1448.5 -754.5Q1491 -777 1519 -798Q1521 -800 1523 -800H1526Q1528 -800 1528 -798V-156Q1528 -116 1546 -88Q1564 -60 1601 -60ZM1995.8 -72Q1998.8 -68 1998.8 -63Q1998.8 -57 1990.8 -52L1960.8 -33Q1933.8 -16 1895.8 -1.5Q1857.8 13 1819.8 13Q1789.8 13 1763.3 -1Q1736.8 -15 1720.8 -48.5Q1704.8 -82 1704.8 -141V-326Q1704.8 -353 1701.8 -379.5Q1698.8 -406 1683.3 -423Q1667.8 -440 1631.8 -440Q1616.8 -440 1616.8 -455V-485Q1616.8 -500 1631.8 -500H1704.8V-567Q1704.8 -580 1713.8 -585Q1735.8 -597 1771.8 -618.5Q1807.8 -640 1835.8 -668Q1837.8 -670 1840.8 -670H1842.8Q1844.8 -670 1844.8 -667V-500H1978.8Q1993.8 -500 1993.8 -485V-442Q1993.8 -427 1978.8 -427H1844.8V-141Q1844.8 -102 1856.8 -87.5Q1868.8 -73 1886.8 -73Q1903.8 -73 1922.8 -82.5Q1941.8 -92 1957.8 -102Q1964.8 -106 1968.8 -106Q1974.8 -106 1979.8 -98Z" fill="currentColor"/></svg>`

var loginPage = template.Must(template.New("login").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="vult-login-csrf" content="{{.CSRF}}"><title>Sign in · Vult</title>` + authHead + `<script src="/auth/particles.js" defer></script>{{if .Passkeys}}<script src="/auth/passkeys.js" defer></script>{{end}}</head><body>` + authBar + `<main>{{if .Passkeys}}<h1>Good to see you.</h1><p>Unlock with your passkey.</p><button id="passkey-signin" type="button">Sign in with a passkey</button><p id="passkey-status" role="status" aria-live="polite"></p>{{if .Authenticator}}<form id="authenticator-signin"><h2>Use an authenticator app instead</h2><label>6-digit code<input id="authenticator-code" name="code" inputmode="numeric" autocomplete="one-time-code" pattern="[0-9]{6}" minlength="6" maxlength="6" required></label><button type="submit">Sign in with the code</button><p id="authenticator-status" role="status" aria-live="polite"></p></form>{{end}}{{if .Recovery}}<p class="fine"><a href="/auth/recover">Use a recovery code</a></p>{{end}}{{end}}{{if .UseForm}}{{if not .Passkeys}}<h1>Good to see you.</h1>{{end}}{{if .Builtin}}<p>Sign in to continue.</p>{{else}}<p>Sign in to continue.</p>{{end}}<form method="post" action="/auth/start"><input type="hidden" name="csrf" value="{{.CSRF}}">{{if .Builtin}}<label>Username<input name="username" autocomplete="username" required maxlength="80"></label><label>Passphrase<input name="password" type="password" autocomplete="current-password" required maxlength="1024"></label><label>Authenticator or recovery code<input name="code" autocomplete="one-time-code" required maxlength="128"></label>{{end}}<button type="submit">{{if .Builtin}}Sign in{{else}}Continue{{end}}</button></form>{{end}}</main></body></html>`))

// signInProblem is the page a browser sees when sign-in can't go on, instead
// of an API error: what happened, in plain words, and the way back.
var signInProblem = template.Must(template.New("sign-in-problem").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Title}} · Vult</title>` + authHead + `<script src="/auth/particles.js" defer></script></head><body>` + authBar + `<main><h1>{{.Title}}</h1><p>{{.Body}}</p><a class="button" href="/auth/login">Back to sign in</a></main></body></html>`))

type ExternalIdentity func(*http.Request) (Identity, error)
type HTTP struct {
	sessions *Service
	builtin  *Builtin
	oidc     *OIDC
	external ExternalIdentity
	page     func(http.ResponseWriter, *http.Request, string)
	passkeys *Passkeys
	local    bool
	agents   LocalAgents
	totp     *LocalAuthenticator
}

type HTTPOptions struct {
	Passkeys *Passkeys
	Local    bool
	// Authenticator adds authenticator-app sign-in for the passkey owner.
	// Local only.
	Authenticator *LocalAuthenticator
	// Agents decides requests from agents on this computer. Local only.
	Agents LocalAgents
}

// NewExternalHTTP lets an identity provider render sign-in while core owns the
// nonce, session cookie and final membership check. The renderer receives only
// the login CSRF nonce, never owner or installation authority.
func NewExternalHTTP(s *Service, external ExternalIdentity, page func(http.ResponseWriter, *http.Request, string)) (*HTTP, error) {
	h, e := NewHTTP(s, nil, nil, external)
	if e != nil || page == nil {
		return nil, ErrDenied
	}
	h.page = page
	return h, nil
}

func NewHTTP(s *Service, b *Builtin, o *OIDC, external ExternalIdentity, options ...HTTPOptions) (*HTTP, error) {
	if len(options) > 1 {
		return nil, ErrDenied
	}
	var p *Passkeys
	local := false
	var agents LocalAgents
	var authenticator *LocalAuthenticator
	if len(options) == 1 {
		p = options[0].Passkeys
		local = options[0].Local
		agents = options[0].Agents
		authenticator = options[0].Authenticator
	}
	if agents != nil && !local {
		return nil, ErrDenied
	}
	if authenticator != nil && (!local || p == nil || authenticator.sessions != s || authenticator.subject != p.subject || authenticator.identityID != p.identityID) {
		return nil, ErrDenied
	}
	n := 0
	if b != nil {
		n++
	}
	if o != nil {
		n++
	}
	if external != nil {
		n++
	}
	if p != nil && b == nil {
		n++
	}
	if s == nil || n != 1 || b != nil && b.sessions != s || o != nil && o.sessions != s || p != nil && (p.sessions != s || o != nil || external != nil || b != nil && b.username != p.subject) {
		return nil, ErrDenied
	}
	if local {
		origin, err := url.Parse(s.origin)
		if err != nil || !loopback.ValidHostname(origin.Hostname()) || p == nil || o != nil || external != nil {
			return nil, ErrDenied
		}
	}
	return &HTTP{sessions: s, builtin: b, oidc: o, external: external, passkeys: p, local: local, agents: agents, totp: authenticator}, nil
}
func cookie(w http.ResponseWriter, name, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: age})
}
func csrf(raw string) string { return Hash("csrf:" + raw) }

// UserID is a stable pseudonymous key for one verified identity. It lets the
// browser keep personal settings apart without learning issuer or subject.
func UserID(id Identity) string { return Hash("user:" + id.Issuer + "\x00" + id.Subject) }
func fail(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, "{\"error\":\"authorization unavailable\"}\n")
}

// failSignIn answers a sign-in step that can't go on. A browser that
// navigated here gets a page with the way back; other callers get the API error.
func failSignIn(w http.ResponseWriter, r *http.Request, status int) {
	if r.Header.Get("Sec-Fetch-Mode") != "navigate" && !strings.Contains(r.Header.Get("Accept"), "text/html") {
		fail(w, status)
		return
	}
	page := struct{ Title, Body string }{"Sign-in is unavailable", "Vult couldn't reach its sign-in just now. Try again in a moment."}
	switch status {
	case http.StatusUnauthorized:
		page = struct{ Title, Body string }{"That sign-in didn't work", "Your details weren't accepted. Try again, or ask whoever runs this Vult."}
	case http.StatusForbidden:
		page = struct{ Title, Body string }{"This sign-in page expired", "For your security, sign-in pages only work for a short time. Start again from the sign-in page."}
	case http.StatusNotFound:
		page = struct{ Title, Body string }{"Sign-in isn't set up", "This Vult has no sign-in method configured yet. Ask whoever runs it."}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = signInProblem.Execute(w, page)
}

func (h *HTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; base-uri 'none'; frame-ancestors 'none'")
	if h.passkeys != nil {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		if h.passkeyRoute(w, r) {
			return
		}
	}
	if h.totp != nil && strings.HasPrefix(r.URL.Path, "/auth/authenticator/") {
		h.authenticatorRoute(w, r)
		return
	}
	if h.agents != nil && r.URL.Path == "/auth/agent" {
		h.agentRoute(w, r)
		return
	}
	if r.URL.Path == "/auth/recovery-codes" && h.localRecovery() != nil {
		h.recoveryRoute(w, r)
		return
	}
	if r.URL.Path == "/auth/recovery.js" && r.Method == http.MethodGet && h.localRecovery() != nil {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = io.WriteString(w, recoveryScript)
		return
	}
	switch {
	case r.URL.Path == "/auth/particles.js" && r.Method == "GET":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = io.WriteString(w, particlesScript)
	case r.URL.Path == "/auth/theme.js" && r.Method == "GET":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = io.WriteString(w, themeScript)
	case r.URL.Path == "/auth/style.css" && r.Method == "GET":
		w.Header().Set("Content-Type", "text/css")
		_, _ = io.WriteString(w, LoginCSS)
	case r.URL.Path == "/auth/login" && r.Method == "GET":
		if !startReviewContinuation(w, r) {
			failSignIn(w, r, http.StatusBadRequest)
			return
		}
		w.Header().Set("Referrer-Policy", "same-origin")
		nonce := Random()
		cookie(w, loginCookie, nonce, 600)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if h.page != nil {
			h.page(w, r, nonce)
			return
		}
		_ = loginPage.Execute(w, struct {
			CSRF          string
			Builtin       bool
			Passkeys      bool
			UseForm       bool
			Recovery      bool
			Authenticator bool
		}{nonce, h.builtin != nil, h.passkeys != nil, h.builtin != nil || h.oidc != nil || h.external != nil, h.passkeys != nil && !h.local, h.totp != nil && h.totp.Ready(r.Context())})
	case r.URL.Path == "/auth/start" && r.Method == "POST":
		h.start(w, r)
	case r.URL.Path == "/auth/start" && r.Method == "GET":
		// Sign-in starts from its page; an address typed or bookmarked goes there.
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
	case r.URL.Path == "/auth/callback" && r.Method == "GET":
		h.callback(w, r)
	case r.URL.Path == "/auth/session" && r.Method == "GET":
		raw, e := r.Cookie(SessionCookie)
		if e != nil {
			fail(w, 401)
			return
		}
		id, e := h.sessions.Identity(r.Context(), raw.Value)
		if e != nil {
			fail(w, 401)
			return
		}
		tenants, e := h.sessions.Tenants(r.Context(), raw.Value)
		if e != nil {
			fail(w, 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		mode := "server"
		if h.local {
			mode = "local"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"mode": mode, "user_id": UserID(id), "tenants": tenants, "csrf": csrf(raw.Value), "agent_address": h.sessions.Resource()})
	case r.URL.Path == "/auth/complete" && r.Method == "GET":
		// Sign-in now opens the Books directly; an older link lands there too.
		http.Redirect(w, r, "/", http.StatusSeeOther)
	case r.URL.Path == "/auth/logout" && r.Method == "POST":
		raw, e := r.Cookie(SessionCookie)
		if e != nil || !h.CheckCSRF(r, raw.Value) {
			fail(w, 403)
			return
		}
		if h.sessions.EndSession(r.Context(), raw.Value) != nil {
			fail(w, 401)
			return
		}
		cookie(w, SessionCookie, "", -1)
		w.WriteHeader(204)
	default:
		fail(w, 404)
	}
}

func (h *HTTP) CheckCSRF(r *http.Request, session string) bool {
	return len(r.Header.Values("Origin")) == 1 && r.Header.Get("Origin") == h.sessions.origin && validToken(session) && len(r.Header.Values("X-CSRF-Token")) == 1 && subtle.ConstantTimeCompare([]byte(csrf(session)), []byte(r.Header.Get("X-CSRF-Token"))) == 1
}

func (h *HTTP) CheckLoginCSRF(r *http.Request) bool {
	c, e := r.Cookie(loginCookie)
	return e == nil && validToken(c.Value) && len(r.Header.Values("Origin")) == 1 && r.Header.Get("Origin") == h.sessions.origin && len(r.Header.Values("X-CSRF-Token")) == 1 && subtle.ConstantTimeCompare([]byte(c.Value), []byte(r.Header.Get("X-CSRF-Token"))) == 1
}

func (h *HTTP) start(w http.ResponseWriter, r *http.Request) {
	if h.oidc == nil && h.builtin == nil && h.external == nil {
		failSignIn(w, r, http.StatusNotFound)
		return
	}
	if len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != h.sessions.origin || len(r.Header.Values("Content-Type")) != 1 || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		failSignIn(w, r, 403)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if r.ParseForm() != nil {
		failSignIn(w, r, 403)
		return
	}
	login, e := r.Cookie(loginCookie)
	if e != nil || !validToken(login.Value) || len(r.PostForm["csrf"]) != 1 || subtle.ConstantTimeCompare([]byte(login.Value), []byte(r.PostForm.Get("csrf"))) != 1 {
		failSignIn(w, r, 403)
		return
	}
	cookie(w, loginCookie, "", -1)
	if h.oidc != nil {
		destination, attempt, e := h.oidc.Start(r.Context())
		if e != nil {
			// The identity provider is unreachable: an outage, not wrong details.
			failSignIn(w, r, http.StatusServiceUnavailable)
			return
		}
		cookie(w, attemptCookie, attempt, 300)
		http.Redirect(w, r, destination, http.StatusSeeOther)
		return
	}
	var session string
	if h.builtin != nil {
		for _, field := range []string{"username", "password", "code"} {
			if len(r.PostForm[field]) != 1 {
				failSignIn(w, r, 403)
				return
			}
		}
		session, e = h.builtin.SignIn(r.Context(), r.PostForm.Get("username"), r.PostForm.Get("password"), r.PostForm.Get("code"))
	} else {
		identity, err := h.external(r)
		if err != nil {
			failSignIn(w, r, 401)
			return
		}
		session, e = h.sessions.IssueSession(r.Context(), identity)
	}
	if e != nil {
		failSignIn(w, r, 401)
		return
	}
	h.signedIn(w, r, session)
}
func (h *HTTP) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	attempt, e := r.Cookie(attemptCookie)
	if h.oidc == nil || e != nil || len(q["state"]) != 1 || len(q["code"]) != 1 || q.Get("error") != "" {
		failSignIn(w, r, 401)
		return
	}
	session, e := h.oidc.Callback(r.Context(), attempt.Value, q.Get("state"), q.Get("code"))
	cookie(w, attemptCookie, "", -1)
	if e != nil {
		failSignIn(w, r, 401)
		return
	}
	h.signedIn(w, r, session)
}
func (h *HTTP) signedIn(w http.ResponseWriter, r *http.Request, session string) {
	h.setSession(w, r, session)
	http.Redirect(w, r, h.signInDestination(w, r, "/"), http.StatusSeeOther)
}

func (h *HTTP) setSession(w http.ResponseWriter, r *http.Request, session string) {
	if old, e := r.Cookie(SessionCookie); e == nil {
		_ = h.sessions.EndSession(r.Context(), old.Value)
	}
	cookie(w, SessionCookie, session, int((2 * time.Hour).Seconds()))
}

func (h *HTTP) signInDestination(w http.ResponseWriter, r *http.Request, fallback string) string {
	if reviews := r.CookiesNamed(reviewCookie); len(reviews) != 0 {
		cookie(w, reviewCookie, "", -1)
		if len(reviews) == 1 {
			if _, _, ok := operationref.Decode(reviews[0].Value); ok {
				return "/auth/approve?operation=" + reviews[0].Value
			}
		}
	}
	if h.local {
		if agents := r.CookiesNamed(agentCookie); len(agents) != 0 {
			cookie(w, agentCookie, "", -1)
			if len(agents) == 1 && validAgentRequest(agents[0].Value) {
				return "/auth/agent?request=" + agents[0].Value
			}
		}
		return "/"
	}
	if pending := r.CookiesNamed(pendingCookie); len(pending) == 1 && validToken(pending[0].Value) {
		return "/oauth/authorize?request=" + pending[0].Value
	}
	return fallback
}

func startReviewContinuation(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.RawQuery == "" {
		if len(r.CookiesNamed(reviewCookie)) != 0 {
			cookie(w, reviewCookie, "", -1)
		}
		return true
	}
	if len(r.URL.RawQuery) > 128 || len(r.Header.Values("Authorization")) != 0 {
		return false
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) != 1 || len(query["operation"]) != 1 {
		return false
	}
	operation := query.Get("operation")
	if _, _, ok := operationref.Decode(operation); !ok {
		return false
	}
	cookie(w, reviewCookie, operation, 300)
	return true
}
